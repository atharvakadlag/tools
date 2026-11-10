package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// newRepo makes a throwaway repo with one commit and points the package globals at it.
func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("one\ntwo\nthree\n"), 0o644)
	run("add", ".")
	run("commit", "-qm", "first")
	optDir = dir
	findRepo()
	return dir
}

func TestUntrackedAndCommits(t *testing.T) {
	dir := newRepo(t)
	os.WriteFile(filepath.Join(dir, "new.txt"), []byte("x\ny"), 0o644) // no trailing newline
	files, _, err := runDiff(Query{A: "HEAD", Untracked: true})
	if err != nil || len(files) != 1 {
		t.Fatalf("files=%v err=%v", files, err)
	}
	f := files[0]
	if !f.Untr || f.St != "A" || f.Add != 2 || len(f.Hunks) != 1 || f.Hunks[0].L[2] != `\ No newline at end of file` {
		t.Fatalf("bad untracked file: %+v %v", f, f.Hunks[0].L)
	}
	if files, _, _ := runDiff(Query{A: "HEAD"}); len(files) != 0 {
		t.Fatalf("untracked shown without the flag: %v", files)
	}
	list, _, recent, err := commitRange(Query{A: "HEAD"})
	if err != nil || !recent || len(list) != 1 || list[0].Subject != "first" || list[0].Parent != emptyTree || list[0].Add != 3 {
		t.Fatalf("commits=%+v recent=%v err=%v", list, recent, err)
	}
}

func TestCommentAnchorsAfterEdit(t *testing.T) {
	dir := newRepo(t)
	c, err := upsertComment(Comment{Path: "a.go", Side: "n", Line: 2, Code: []string{"two"}, Text: "why two?"})
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("zero\none\ntwo\nthree\n"), 0o644)
	if got := anchor(c); got != 3 {
		t.Fatalf("anchor = %d, want 3", got)
	}
	md := commentsMarkdown(loadComments(), false)
	if !strings.Contains(md, "`a.go:3`") || !strings.Contains(md, "why two?") || !strings.Contains(md, c.ID) {
		t.Fatalf("markdown:\n%s", md)
	}
	if n, _ := resolveComments([]string{c.ID}, true); n != 1 || !strings.Contains(commentsMarkdown(loadComments(), false), "No open") {
		t.Fatal("resolve failed")
	}
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("gone\n"), 0o644)
	if got := anchor(c); got != 0 {
		t.Fatalf("anchor of removed code = %d, want 0", got)
	}
}

func TestAddCommentAndSnapshotDiff(t *testing.T) {
	dir := newRepo(t)
	c, err := addComment("a.go:2-3", "", "claude", "high", false, "check this")
	if err != nil || c.Author != "claude" || c.Severity != "high" || strings.Join(c.Code, ",") != "two,three" {
		t.Fatalf("addComment = %+v, %v", c, err)
	}
	if _, err := addComment("a.go:9", "", "", "", false, "x"); err == nil {
		t.Fatal("expected out-of-range line to fail")
	}
	before, err := recordSnapshot("before")
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("one\nTWO\nthree\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "u.txt"), []byte("new\n"), 0o644)
	files, _, err := runDiff(Query{A: before.Tree, B: wtSnap})
	if err != nil || len(files) != 2 || files[0].New != "a.go" || files[1].New != "u.txt" || files[1].St != "A" {
		t.Fatalf("snapshot diff = %+v, %v", files, err)
	}
	if out, _ := exec.Command("git", "-C", dir, "status", "--porcelain").Output(); !strings.Contains(string(out), "?? u.txt") {
		t.Fatalf("snapshot touched the real index: %s", out)
	}
	after, _ := recordSnapshot("")
	if after.Tree == before.Tree || after.Files != 2 || after.Add != 2 || after.Del != 1 {
		t.Fatalf("after = %+v", after)
	}
}

func TestSemanticDiff(t *testing.T) {
	old := []byte("package p\n\nfunc keep() int { return 1 }\n\nfunc gone() {}\n\nfunc oldName(a int) int {\n\treturn a * 2\n}\n\ntype T struct{}\n\nfunc (t *T) M(x int) {}\n")
	new := []byte("package p\n\nfunc keep() int { return 2 }\n\nfunc newName(a int) int {\n\treturn a * 2\n}\n\ntype T struct{}\n\nfunc (t *T) M(x, y int) {}\n\nfunc added() {\n\tf := func() {}\n\t_ = f\n}\n")
	f := &File{Old: "p.go", New: "p.go", St: "M", Hunks: []*Hunk{{OS: 1, NS: 1, L: diffLines(old, new)}}}
	got := map[string]SymChange{}
	for _, c := range semanticDiff(f, old, new) {
		got[c.Change+" "+c.Qual+"."+c.Name] = c
	}
	for _, want := range []string{"modified .keep", "removed .gone", "renamed .newName", "modified T.M", "added .added"} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing %q in %v", want, got)
		}
	}
	if got["modified .keep"].Sig {
		t.Error("one-line body change reported as a signature change")
	}
	if !got["modified T.M"].Sig || got["renamed .newName"].From != "oldName" {
		t.Errorf("sig/rename details wrong: %+v %+v", got["modified T.M"], got["renamed .newName"])
	}
	if len(got) != 5 {
		t.Errorf("want 5 changes (no closure locals), got %v", got)
	}
}

// diffLines is a crude whole-file "diff" (all old lines removed, all new added) — enough for
// the semantic pass, which only needs to know which lines changed.
func diffLines(old, new []byte) []string {
	var l []string
	for _, s := range strings.Split(strings.TrimSuffix(string(old), "\n"), "\n") {
		l = append(l, "-"+s)
	}
	for _, s := range strings.Split(strings.TrimSuffix(string(new), "\n"), "\n") {
		l = append(l, "+"+s)
	}
	return l
}

func TestGoCoverage(t *testing.T) {
	dir := newRepo(t)
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/m\n"), 0o644)
	exec.Command("git", "-C", dir, "add", "go.mod").Run()
	d := &covData{Lines: map[string]map[int]bool{}}
	parseGoCover([]byte("mode: set\nexample.com/m/a.go:1.1,2.5 2 1\nexample.com/m/a.go:3.1,3.9 1 0\n"), d)
	if !d.Lines["a.go"][1] || !d.Lines["a.go"][2] || d.Lines["a.go"][3] {
		t.Fatalf("lines = %v", d.Lines)
	}
}
