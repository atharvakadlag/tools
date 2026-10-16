package main

// Code navigation: go-to-definition / find-references using tree-sitter.
//
// Candidates come from `git grep -w` (at the requested revision), then each candidate
// file is parsed and the grammar's tags.scm query picks out definitions; references are
// identifier nodes with matching text (so strings and comments are excluded).
// Resolution is by name, like GitHub's search-based code navigation.

import (
	"bufio"
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	ts "github.com/tree-sitter/go-tree-sitter"
	tsc "github.com/tree-sitter/tree-sitter-c/bindings/go"
	tscs "github.com/tree-sitter/tree-sitter-c-sharp/bindings/go"
	tscpp "github.com/tree-sitter/tree-sitter-cpp/bindings/go"
	tsgo "github.com/tree-sitter/tree-sitter-go/bindings/go"
	tsjava "github.com/tree-sitter/tree-sitter-java/bindings/go"
	tsjs "github.com/tree-sitter/tree-sitter-javascript/bindings/go"
	tspy "github.com/tree-sitter/tree-sitter-python/bindings/go"
	tsrb "github.com/tree-sitter/tree-sitter-ruby/bindings/go"
	tsrs "github.com/tree-sitter/tree-sitter-rust/bindings/go"
	tsscala "github.com/tree-sitter/tree-sitter-scala/bindings/go"
	tsts "github.com/tree-sitter/tree-sitter-typescript/bindings/go"
)

//go:embed queries/*.scm
var queryFS embed.FS

type langDef struct {
	ptr     func() unsafe.Pointer
	queries []string // tags query files, concatenated (first that compiles wins)
	once    sync.Once
	lang    *ts.Language
	query   *ts.Query
	nameIdx uint
}

var langs = map[string]*langDef{
	"python":     {ptr: tspy.Language, queries: []string{"python"}},
	"go":         {ptr: tsgo.Language, queries: []string{"go"}},
	"javascript": {ptr: tsjs.Language, queries: []string{"javascript"}},
	"typescript": {ptr: tsts.LanguageTypescript, queries: []string{"javascript+typescript", "typescript"}},
	"tsx":        {ptr: tsts.LanguageTSX, queries: []string{"javascript+typescript", "typescript"}},
	"java":       {ptr: tsjava.Language, queries: []string{"java"}},
	"rust":       {ptr: tsrs.Language, queries: []string{"rust"}},
	"ruby":       {ptr: tsrb.Language, queries: []string{"ruby"}},
	"c":          {ptr: tsc.Language, queries: []string{"c"}},
	"cpp":        {ptr: tscpp.Language, queries: []string{"cpp"}},
	"csharp":     {ptr: tscs.Language, queries: []string{"csharp"}},
	"scala":      {ptr: tsscala.Language, queries: []string{"scala"}},
}

var extLang = map[string]string{
	".py": "python", ".pyi": "python", ".go": "go",
	".js": "javascript", ".mjs": "javascript", ".cjs": "javascript", ".jsx": "javascript",
	".ts": "typescript", ".mts": "typescript", ".cts": "typescript", ".tsx": "tsx",
	".java": "java", ".rs": "rust", ".rb": "ruby", ".rake": "ruby",
	".c": "c", ".h": "c", ".cc": "cpp", ".cpp": "cpp", ".cxx": "cpp", ".hpp": "cpp", ".hh": "cpp",
	".cs": "csharp", ".scala": "scala", ".sc": "scala", ".sbt": "scala",
}

func langFor(path string) *langDef {
	l := langs[extLang[strings.ToLower(filepath.Ext(path))]]
	if l == nil {
		return nil
	}
	l.once.Do(func() {
		l.lang = ts.NewLanguage(l.ptr())
		for _, spec := range l.queries {
			var src strings.Builder
			for _, part := range strings.Split(spec, "+") {
				b, _ := queryFS.ReadFile("queries/" + part + ".scm")
				src.Write(b)
				src.WriteByte('\n')
			}
			q, err := ts.NewQuery(l.lang, src.String())
			if err != nil {
				continue
			}
			if i, ok := q.CaptureIndexForName("name"); ok {
				l.query, l.nameIdx = q, i
				break
			}
			q.Close()
		}
	})
	return l
}

type Loc struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Col  int    `json:"col"`
	Kind string `json:"kind"`
	Text string `json:"text"`
}

type NavResult struct {
	Name      string `json:"name"`
	Rev       string `json:"rev"`
	Defs      []Loc  `json:"defs"`
	Refs      []Loc  `json:"refs"`
	Files     int    `json:"files"`
	Truncated bool   `json:"truncated,omitempty"`
	Ms        int64  `json:"ms"`
}

var identRe = regexp.MustCompile(`^[\p{L}_$][\p{L}\p{N}_$]*$`)

const (
	navMaxFiles = 1500
	navMaxSize  = 2 << 20
	navMaxRefs  = 3000
)

func isIdentKind(k string) bool {
	return strings.Contains(k, "identifier") || k == "constant" || k == "name" || k == "type"
}

func isWordByte(b byte) bool {
	return b == '_' || b == '$' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= 0x80
}

// occurrences returns byte offsets of whole-word matches of name in src.
func occurrences(src []byte, name string) []int {
	var out []int
	nb := []byte(name)
	for i := 0; ; {
		j := bytes.Index(src[i:], nb)
		if j < 0 {
			return out
		}
		at := i + j
		end := at + len(nb)
		if (at == 0 || !isWordByte(src[at-1])) && (end == len(src) || !isWordByte(src[end])) {
			out = append(out, at)
		}
		i = end
	}
}

func lineAt(src []byte, off int) (line int, text string) {
	line = bytes.Count(src[:off], []byte{'\n'}) + 1
	s := bytes.LastIndexByte(src[:off], '\n') + 1
	e := bytes.IndexByte(src[off:], '\n')
	if e < 0 {
		e = len(src)
	} else {
		e += off
	}
	t := strings.TrimRight(string(src[s:e]), "\r")
	if len(t) > 400 {
		t = t[:400]
	}
	return line, t
}

func analyzeFile(path string, src []byte, name string) (defs, refs []Loc) {
	occ := occurrences(src, name)
	if len(occ) == 0 {
		return nil, nil
	}
	mk := func(off int, kind string) Loc {
		ln, txt := lineAt(src, off)
		col := off - (bytes.LastIndexByte(src[:off], '\n') + 1) + 1
		return Loc{Path: path, Line: ln, Col: col, Kind: kind, Text: txt}
	}
	l := langFor(path)
	if l == nil || l.query == nil {
		seen := map[int]bool{}
		for _, off := range occ {
			if loc := mk(off, "text"); !seen[loc.Line] {
				seen[loc.Line] = true
				refs = append(refs, loc)
			}
		}
		return nil, refs
	}
	p := ts.NewParser()
	defer p.Close()
	if err := p.SetLanguage(l.lang); err != nil {
		return nil, nil
	}
	tree := p.Parse(src, nil)
	if tree == nil {
		return nil, nil
	}
	defer tree.Close()
	root := tree.RootNode()

	defAt := map[uint]bool{}
	refKind := map[uint]string{}
	qc := ts.NewQueryCursor()
	defer qc.Close()
	qc.SetTimeoutMicros(2_000_000)
	names := l.query.CaptureNames()
	ms := qc.Matches(l.query, root, src)
	for m := ms.Next(); m != nil; m = ms.Next() {
		var nameNode *ts.Node
		kind := ""
		for i := range m.Captures {
			c := &m.Captures[i]
			cn := names[c.Index]
			if c.Index == uint32(l.nameIdx) {
				nameNode = &c.Node
			} else if strings.HasPrefix(cn, "definition.") || strings.HasPrefix(cn, "reference.") {
				kind = cn
			}
		}
		if nameNode == nil || kind == "" || nameNode.Utf8Text(src) != name {
			continue
		}
		at := nameNode.StartByte()
		if k, ok := strings.CutPrefix(kind, "definition."); ok {
			if k == "function" && insideClass(nameNode) {
				k = "method"
			}
			if !defAt[at] {
				defAt[at] = true
				defs = append(defs, mk(int(at), k))
			}
		} else {
			refKind[at] = strings.TrimPrefix(kind, "reference.")
		}
	}
	for _, off := range occ {
		at := uint(off)
		if defAt[at] {
			continue
		}
		n := root.DescendantForByteRange(at, at+uint(len(name)))
		if n == nil || n.StartByte() != at || n.EndByte() != at+uint(len(name)) || !isIdentKind(n.Kind()) {
			continue
		}
		k := refKind[at]
		if k == "" {
			k = "usage"
		}
		refs = append(refs, mk(off, k))
	}
	return defs, refs
}

// insideClass reports whether a definition sits in a class body (tags queries for
// e.g. Python don't distinguish methods from functions).
func insideClass(n *ts.Node) bool {
	def := n.Parent() // the definition node itself
	if def == nil {
		return false
	}
	for p := def.Parent(); p != nil; p = p.Parent() {
		k := p.Kind()
		if strings.Contains(k, "class") {
			return true
		}
		if strings.Contains(k, "function") || strings.Contains(k, "method") {
			return false // a closure inside another function
		}
	}
	return false
}

// resolveRev turns a ref into a commit sha; "" means the working tree.
func resolveRev(rev string) (string, error) {
	if rev == "" {
		return "", nil
	}
	if strings.HasPrefix(rev, "-") {
		return "", errors.New("bad rev")
	}
	out, err := git("rev-parse", "--verify", "--quiet", "--end-of-options", rev+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("unknown rev %q", rev)
	}
	return strings.TrimSpace(string(out)), nil
}

// grepFiles lists repo-relative files containing name as a whole word.
func grepFiles(sha, name string) []string {
	args := []string{"-C", repoRoot, "-c", "core.quotePath=false", "grep", "-l", "-w", "-F", "-I", "-e", name}
	if sha == "" {
		args = append(args, "--untracked")
	} else {
		args = append(args, sha)
	}
	args = append(args, "--")
	out, _ := git(args...) // exit 1 = no matches
	var files []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l == "" {
			continue
		}
		if sha != "" {
			l = strings.TrimPrefix(l, sha+":")
		}
		files = append(files, l)
	}
	return files
}

// catFiles streams blobs for sha:path via one `git cat-file --batch`.
func catFiles(sha string, paths []string, fn func(path string, data []byte)) error {
	cmd := exec.Command("git", "-C", repoRoot, "cat-file", "--batch")
	in, _ := cmd.StdinPipe()
	outp, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() {
		w := bufio.NewWriter(in)
		for _, p := range paths {
			fmt.Fprintf(w, "%s:%s\n", sha, p)
		}
		w.Flush()
		in.Close()
	}()
	r := bufio.NewReaderSize(outp, 1<<16)
	for _, p := range paths {
		hdr, err := r.ReadString('\n')
		if err != nil {
			break
		}
		f := strings.Fields(hdr)
		if len(f) < 3 || f[1] != "blob" {
			continue // "missing"
		}
		size, _ := strconv.Atoi(f[2])
		buf := make([]byte, size+1)
		if _, err := io.ReadFull(r, buf); err != nil {
			break
		}
		if size <= navMaxSize {
			fn(p, buf[:size])
		}
	}
	io.Copy(io.Discard, r)
	return cmd.Wait()
}

func readWorktree(path string) ([]byte, error) {
	full := filepath.Join(repoRoot, filepath.FromSlash(path))
	if rel, err := filepath.Rel(repoRoot, full); err != nil || strings.HasPrefix(rel, "..") {
		return nil, errors.New("path outside repo")
	}
	st, err := os.Stat(full)
	if err != nil {
		return nil, err
	}
	if st.Size() > navMaxSize {
		return nil, errors.New("file too large")
	}
	return os.ReadFile(full)
}

func navigate(name, rev, from string) (*NavResult, error) {
	start := time.Now()
	if !identRe.MatchString(name) || len(name) > 200 {
		return nil, errors.New("not an identifier")
	}
	sha, err := resolveRev(rev)
	if err != nil {
		return nil, err
	}
	files := grepFiles(sha, name)
	res := &NavResult{Name: name, Rev: rev, Defs: []Loc{}, Refs: []Loc{}, Files: len(files)}
	if len(files) > navMaxFiles {
		files, res.Truncated = files[:navMaxFiles], true
	}

	type job struct {
		path string
		data []byte
	}
	jobs := make(chan job, 64)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < runtime.NumCPU(); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				d, r := analyzeFile(j.path, j.data, name)
				mu.Lock()
				res.Defs = append(res.Defs, d...)
				res.Refs = append(res.Refs, r...)
				mu.Unlock()
			}
		}()
	}
	if sha == "" {
		for _, p := range files {
			if b, err := readWorktree(p); err == nil {
				jobs <- job{p, b}
			}
		}
	} else {
		catFiles(sha, files, func(p string, b []byte) { jobs <- job{p, b} })
	}
	close(jobs)
	wg.Wait()

	// definitions in the file you clicked from first, then by path; refs grouped by file
	sort.Slice(res.Defs, func(i, j int) bool {
		a, b := res.Defs[i], res.Defs[j]
		if (a.Path == from) != (b.Path == from) {
			return a.Path == from
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.Line < b.Line
	})
	sort.Slice(res.Refs, func(i, j int) bool {
		a, b := res.Refs[i], res.Refs[j]
		if (a.Path == from) != (b.Path == from) {
			return a.Path == from
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.Line < b.Line || a.Line == b.Line && a.Col < b.Col
	})
	if len(res.Refs) > navMaxRefs {
		res.Refs, res.Truncated = res.Refs[:navMaxRefs], true
	}
	res.Ms = time.Since(start).Milliseconds()
	return res, nil
}

// readAt returns a file's contents at rev ("" = working tree).
func readAt(rev, path string) ([]byte, error) {
	path = strings.TrimPrefix(filepath.ToSlash(filepath.Clean("/"+path)), "/")
	sha, err := resolveRev(rev)
	if err != nil {
		return nil, err
	}
	if sha == "" {
		return readWorktree(path)
	}
	var data []byte
	catFiles(sha, []string{path}, func(_ string, b []byte) { data = b })
	if data == nil {
		return nil, fmt.Errorf("%s not found at %s", path, rev)
	}
	return data, nil
}
