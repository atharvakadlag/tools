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
