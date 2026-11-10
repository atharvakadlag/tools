package main

// Semantic change map: which symbols (functions, methods, types, classes) a diff adds, removes,
// renames or modifies, found by parsing the old and new version of each changed file with the
// same tree-sitter tag queries used for navigation. For changed signatures and removed/renamed
// symbols it also counts the references that remain in the new code — the "blast radius", and
// call sites a rename or removal may have left behind.

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"

	ts "github.com/tree-sitter/go-tree-sitter"
)

type sym struct {
	key, name, qual, kind string
	start, end            int // 1-based line range of the whole definition
	sig                   string
	body, anon            string // normalized body; body with its own name blanked (rename detection)
}

type SymChange struct {
	Name     string `json:"name"`
	Qual     string `json:"qual,omitempty"`
	Kind     string `json:"kind"`
	Change   string `json:"change"` // added | removed | modified | renamed
	From     string `json:"from,omitempty"`
	Line     int    `json:"line,omitempty"` // new side
	OldLine  int    `json:"oldLine,omitempty"`
	Add      int    `json:"add"`
	Del      int    `json:"del"`
	Sig      bool   `json:"sig,omitempty"` // signature line changed
	OldSig   string `json:"oldSig,omitempty"`
	NewSig   string `json:"newSig,omitempty"`
	Refs     int    `json:"refs"` // references to it in the new code, outside its own definition (-1 = not computed)
	RefFiles int    `json:"refFiles,omitempty"`
}

type SemFile struct {
	Path string      `json:"path"`
	Syms []SymChange `json:"syms"`
}

var (
	wsRe   = regexp.MustCompile(`\s+`)
	recvRe = regexp.MustCompile(`([A-Za-z_]\w*)\s*(?:\[[^\]]*\])?\s*\)\s*$`)
)

// symbols extracts top-level and nested definitions from src.
func symbols(path string, src []byte) []sym {
	l := langFor(path)
	if l == nil || l.query == nil || len(src) > navMaxSize {
		return nil
	}
	p := ts.NewParser()
	defer p.Close()
	if p.SetLanguage(l.lang) != nil {
		return nil
	}
	tree := p.Parse(src, nil)
	if tree == nil {
		return nil
	}
	defer tree.Close()
	qc := ts.NewQueryCursor()
	defer qc.Close()
	qc.SetTimeoutMicros(2_000_000)
	names := l.query.CaptureNames()
	var out []sym
	seen := map[string]int{}
	ms := qc.Matches(l.query, tree.RootNode(), src)
	for m := ms.Next(); m != nil; m = ms.Next() {
		var nameNode, def *ts.Node
		kind := ""
		for i := range m.Captures {
			c := &m.Captures[i]
			if c.Index == uint32(l.nameIdx) {
				n := c.Node
				nameNode = &n
			} else if k, ok := strings.CutPrefix(names[c.Index], "definition."); ok {
				n := c.Node
				def, kind = &n, k
			}
		}
		if nameNode == nil || def == nil {
			continue
		}
		if inFunctionBody(def) {
			continue // locals: closures, types declared inside a function
		}
		name := nameNode.Utf8Text(src)
		if kind == "function" && insideClass(nameNode) {
			kind = "method"
		}
		text := def.Utf8Text(src)
		s := sym{name: name, kind: kind, qual: qualifier(def, src),
			start: int(def.StartPosition().Row) + 1, end: int(def.EndPosition().Row) + 1}
		first := strings.SplitN(text, "\n", 2)[0]
		if i := strings.Index(first, "{"); i > 0 { // "func f() { return x }" — the body isn't the signature
			first = first[:i]
		}
		s.sig = strings.TrimSpace(first)
		s.body = wsRe.ReplaceAllString(text, " ")
		s.anon = strings.Replace(s.body, name, "\x00", 1)
		base := s.kind + ":" + s.qual + "." + name
		seen[base]++
		s.key = base
		if n := seen[base]; n > 1 { // overloads / redefinitions
			s.key = fmt.Sprintf("%s#%d", base, n)
		}
		out = append(out, s)
	}
	return out
}

func inFunctionBody(def *ts.Node) bool {
	for p := def.Parent(); p != nil; p = p.Parent() {
		k := p.Kind()
		if strings.Contains(k, "class") || strings.Contains(k, "impl") || strings.Contains(k, "interface") || strings.Contains(k, "trait") {
			return false
		}
		if strings.Contains(k, "function") || strings.Contains(k, "method") || strings.Contains(k, "lambda") || k == "block" && p.Parent() != nil && strings.Contains(p.Parent().Kind(), "func") {
			return true
		}
	}
	return false
}

// qualifier names the type a method belongs to (Go receiver, or the enclosing class/impl).
func qualifier(def *ts.Node, src []byte) string {
	if def.Kind() == "method_declaration" { // Go
		if r := def.ChildByFieldName("receiver"); r != nil {
			if m := recvRe.FindStringSubmatch(r.Utf8Text(src)); m != nil {
				return m[1]
			}
		}
	}
	for p := def.Parent(); p != nil; p = p.Parent() {
		k := p.Kind()
		if strings.Contains(k, "class") || strings.Contains(k, "interface") || strings.Contains(k, "trait") ||
			k == "impl_item" || k == "object_definition" || k == "module" || k == "struct_item" {
			n := p.ChildByFieldName("name")
			if n == nil {
				n = p.ChildByFieldName("type")
			}
			if n != nil {
				return n.Utf8Text(src)
			}
		}
		if strings.Contains(k, "function") || strings.Contains(k, "method") {
			return "" // closure
		}
	}
	return ""
}

// changedLines returns the deleted (old numbering) and added (new numbering) lines of f.
func changedLines(f *File) (del, add map[int]bool) {
	del, add = map[int]bool{}, map[int]bool{}
	for _, h := range f.Hunks {
		o, n := h.OS, h.NS
		for _, l := range h.L {
			if l == "" {
				continue
			}
			switch l[0] {
			case '-':
				del[o] = true
				o++
			case '+':
				add[n] = true
				n++
			case '\\':
			default:
				o++
				n++
			}
		}
	}
	return
}

// innermost marks, for each line, the smallest symbol containing it.
func innermost(syms []sym, lines map[int]bool) map[string]bool {
	hit := map[string]bool{}
	for ln := range lines {
		best := -1
		for i, s := range syms {
			if s.start <= ln && ln <= s.end && (best < 0 || s.end-s.start < syms[best].end-syms[best].start) {
				best = i
			}
		}
		if best >= 0 {
			hit[syms[best].key] = true
		}
	}
	return hit
}

func countIn(lines map[int]bool, a, b int) int {
	n := 0
	for ln := range lines {
		if a <= ln && ln <= b {
			n++
		}
	}
	return n
}

func semanticDiff(f *File, oldSrc, newSrc []byte) []SymChange {
	path := filePath(f)
	var os_, ns []sym
	if oldSrc != nil {
		os_ = symbols(f.Old, oldSrc)
	}
	if newSrc != nil {
		ns = symbols(path, newSrc)
	}
	del, add := changedLines(f)
	touchedOld, touchedNew := innermost(os_, del), innermost(ns, add)
	oldBy, newBy := map[string]sym{}, map[string]sym{}
	for _, s := range os_ {
		oldBy[s.key] = s
	}
	for _, s := range ns {
		newBy[s.key] = s
	}
	var out []SymChange
	var added, removed []sym
	for _, s := range ns {
		o, ok := oldBy[s.key]
		if !ok {
			added = append(added, s)
			continue
		}
		if (touchedNew[s.key] || touchedOld[s.key]) && o.body != s.body {
			c := SymChange{Name: s.name, Qual: s.qual, Kind: s.kind, Change: "modified", Line: s.start, OldLine: o.start,
				Add: countIn(add, s.start, s.end), Del: countIn(del, o.start, o.end), Refs: -1}
			if o.sig != s.sig {
				c.Sig, c.OldSig, c.NewSig = true, o.sig, s.sig
			}
			out = append(out, c)
		}
	}
	for _, s := range os_ {
		if _, ok := newBy[s.key]; !ok {
			removed = append(removed, s)
		}
	}
	// a removed + added pair with the same kind and the same body apart from the name is a rename
	used := map[int]bool{}
	for _, r := range removed {
		matched := false
		for i, a := range added {
			if !used[i] && a.kind == r.kind && a.anon == r.anon && a.name != r.name {
				used[i], matched = true, true
				out = append(out, SymChange{Name: a.name, Qual: a.qual, Kind: a.kind, Change: "renamed", From: r.name,
					Line: a.start, OldLine: r.start, Add: countIn(add, a.start, a.end), Del: countIn(del, r.start, r.end), Refs: -1})
				break
			}
		}
		if !matched {
			out = append(out, SymChange{Name: r.name, Qual: r.qual, Kind: r.kind, Change: "removed", OldLine: r.start,
				Del: countIn(del, r.start, r.end), Refs: -1})
		}
	}
	for i, a := range added {
		if !used[i] {
			out = append(out, SymChange{Name: a.name, Qual: a.qual, Kind: a.kind, Change: "added", Line: a.start,
				Add: countIn(add, a.start, a.end), Refs: -1})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		li, lj := out[i].Line, out[j].Line
		if li == 0 {
			li = out[i].OldLine
		}
		if lj == 0 {
			lj = out[j].OldLine
		}
		return li < lj
	})
	if len(out) > 300 {
		out = out[:300]
	}
	return out
}

func catBlob(id string) []byte {
	if id == "" {
		return nil
	}
	b, err := git("cat-file", "blob", id)
	if err != nil {
		return nil
	}
	return b
}

// sides returns the old and new contents of a diffed file (nil where the side doesn't exist).
func sides(f *File, q Query) (oldSrc, newSrc []byte) {
	if f.St != "A" {
		if oldSrc = catBlob(f.OB); oldSrc == nil {
			rev := strings.Split(q.A, "..")[0]
			oldSrc, _ = readAt(rev, f.Old)
		}
	}
	if f.St != "D" {
		if newSrc = catBlob(f.NB); newSrc == nil && f.WT {
			newSrc, _ = readWorktree(f.New)
		}
	}
	return
}

var (
	semMu    sync.Mutex
	semCache = map[string][]SymChange{} // by file content key
	refCache = map[string][2]int{}      // rev\x00name\x00path\x00line\x00stamp → refs, files
)

// semantic computes the change map for a query's files, then counts references for the
// symbols whose callers matter most (changed signatures, removals, renames).
func semantic(q Query, files []*File, stamp string) []SemFile {
	var out []SemFile
	type job struct{ fi, si int }
	var jobs []job
	for _, f := range files {
		if f.Bin || len(f.Hunks) == 0 || langFor(filePath(f)) == nil {
			continue
		}
		semMu.Lock()
		syms, ok := semCache[f.K]
		semMu.Unlock()
		if !ok {
			o, n := sides(f, q)
			syms = semanticDiff(f, o, n)
			semMu.Lock()
			if len(semCache) > 5000 {
				semCache = map[string][]SymChange{}
			}
			semCache[f.K] = syms
			semMu.Unlock()
		}
		if len(syms) == 0 {
			continue
		}
		cp := append([]SymChange(nil), syms...)
		out = append(out, SemFile{Path: filePath(f), Syms: cp})
		for si, s := range cp {
			if (s.Change == "removed" || s.Change == "renamed" || s.Sig) && len(jobs) < 40 {
				jobs = append(jobs, job{len(out) - 1, si})
			}
		}
	}
	rev := q.B
	if q.Staged || q.B == "" || q.B == wtSnap || strings.Contains(q.A, "..") {
		rev = "" // working tree (close enough for staged and ranges)
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for _, j := range jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func(j job) {
			defer wg.Done()
			defer func() { <-sem }()
			sf := &out[j.fi]
			s := &sf.Syms[j.si]
			name := s.Name
			if s.Change == "renamed" {
				name = s.From // who still uses the old name?
			}
			k := strings.Join([]string{rev, name, sf.Path, fmt.Sprint(s.Line), stamp}, "\x00")
			semMu.Lock()
			v, ok := refCache[k]
			semMu.Unlock()
			if !ok {
				v = [2]int{0, 0}
				if res, err := navigate(name, rev, sf.Path); err == nil {
					filesSeen := map[string]bool{}
					for _, r := range res.Refs { // navigate lists definitions separately, so these are uses
						v[0]++
						filesSeen[r.Path] = true
					}
					v[1] = len(filesSeen)
				}
				semMu.Lock()
				if len(refCache) > 5000 {
					refCache = map[string][2]int{}
				}
				refCache[k] = v
				semMu.Unlock()
			}
			s.Refs, s.RefFiles = v[0], v[1]
		}(j)
	}
	wg.Wait()
	return out
}
