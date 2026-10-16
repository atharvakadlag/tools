package main

import (
	"strings"
	"testing"
)

func TestQueriesCompile(t *testing.T) {
	for ext, name := range extLang {
		l := langFor("x" + ext)
		if l == nil || l.query == nil {
			t.Errorf("%s (%s): tags query failed to compile", name, ext)
		}
	}
}

func kinds(locs []Loc) string {
	var s []string
	for _, l := range locs {
		s = append(s, l.Kind+"@"+itoa(l.Line))
	}
	return strings.Join(s, " ")
}

func itoa(i int) string { return strings.TrimSpace(strings.Repeat(" ", 0) + string(rune('0'+i/10%10)) + string(rune('0'+i%10))) }

func TestAnalyze(t *testing.T) {
	cases := []struct{ path, src, name, defs, refs string }{
		{"a.py", `class Pipeline:
    def run(self):
        return self.helper()   # helper() in comment
    def helper(self):
        s = "helper"
        return helper
`, "helper", "method@04", "call@03 usage@06"},
		{"a.py", "def run():\n    pass\n", "Pipeline", "", ""},
		{"m.go", `package m
func Fetch() {}
type Repo struct{}
func (r *Repo) Fetch() { Fetch() } // Fetch
var s = "Fetch"
`, "Fetch", "function@02 method@04", "call@04"},
		{"x.ts", `export class Svc { load(): void {} }
function load() { new Svc().load(); }
const msg = "load";
`, "load", "method@01 function@02", "call@02"},
		{"notes.txt", "see helper here\nhelper again\n", "helper", "", "text@01 text@02"},
	}
	for _, c := range cases {
		d, r := analyzeFile(c.path, []byte(c.src), c.name)
		if got := kinds(d); got != c.defs {
			t.Errorf("%s %s defs: got %q want %q", c.path, c.name, got, c.defs)
		}
		if got := kinds(r); got != c.refs {
			t.Errorf("%s %s refs: got %q want %q", c.path, c.name, got, c.refs)
		}
	}
}
