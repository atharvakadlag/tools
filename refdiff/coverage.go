package main

// Test coverage overlay. refdiff looks for a coverage report in the repo (or the file given
// with --coverage), parses it, and tells the page which changed lines tests execute. Rerunning
// tests rewrites the report, and the page updates.
//
// Formats: Go cover profiles (go test -coverprofile), lcov (lcov.info), coverage.py JSON
// (coverage json), and Cobertura XML (coverage.xml, used by pytest-cov, JaCoCo converters, …).

import (
	"bufio"
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

var optCoverage string // --coverage <file>

var covCandidates = []string{
	"cover.out", "coverage.out", "c.out", "cover.profile", "coverage.txt",
	"lcov.info", "coverage/lcov.info", "coverage.lcov",
	"coverage.json", "coverage.xml", "coverage/cobertura-coverage.xml", "build/reports/cobertura.xml",
}

type covData struct {
	File  string                  // repo-relative path of the report
	Mod   time.Time               // report mtime
	Lines map[string]map[int]bool // repo path → line → executed
	Err   string
}

var (
	covMu    sync.Mutex
	covCache *covData
	covKey   string
)

func covFile() (string, os.FileInfo) {
	if optCoverage != "" {
		p := optCoverage
		if !filepath.IsAbs(p) {
			p = filepath.Join(repoRoot, p)
		}
		st, err := os.Stat(p)
		if err != nil {
			return p, nil
		}
		return p, st
	}
	var best string
	var bestSt os.FileInfo
	for _, c := range covCandidates {
		p := filepath.Join(repoRoot, c)
		if st, err := os.Stat(p); err == nil && !st.IsDir() && (bestSt == nil || st.ModTime().After(bestSt.ModTime())) {
			best, bestSt = p, st
		}
	}
	return best, bestSt
}

// covStamp changes whenever the coverage report appears, disappears or is rewritten.
func covStamp() string {
	p, st := covFile()
	if st == nil {
		return "none"
	}
	return p + "|" + strconv.FormatInt(st.ModTime().UnixNano(), 36) + "|" + strconv.FormatInt(st.Size(), 36)
}

func loadCoverage() *covData {
	covMu.Lock()
	defer covMu.Unlock()
	k := covStamp()
	if covCache != nil && covKey == k {
		return covCache
	}
	covKey = k
	p, st := covFile()
	if st == nil {
		covCache = nil
		return nil
	}
	d := &covData{Mod: st.ModTime(), Lines: map[string]map[int]bool{}}
	if r, err := filepath.Rel(repoRoot, p); err == nil && !strings.HasPrefix(r, "..") {
		d.File = r
	} else {
		d.File = p
	}
	data, err := os.ReadFile(p)
	if err == nil {
		err = parseCoverage(p, data, d)
	}
	if err != nil {
		d.Err = err.Error()
	}
	covCache = d
	return d
}

func (d *covData) mark(file string, line int, hit bool) {
	file = d.repoPath(file)
	if file == "" {
		return
	}
	m := d.Lines[file]
	if m == nil {
		m = map[int]bool{}
		d.Lines[file] = m
	}
	m[line] = m[line] || hit
}

// repoPath maps a report path (absolute, repo-relative, or relative to a subdir) to a repo path.
func (d *covData) repoPath(p string) string {
	p = filepath.ToSlash(p)
	if filepath.IsAbs(p) {
		r, err := filepath.Rel(repoRoot, p)
		if err != nil || strings.HasPrefix(r, "..") {
			return ""
		}
		return filepath.ToSlash(r)
	}
	return strings.TrimPrefix(path.Clean(p), "./")
}

func parseCoverage(p string, data []byte, d *covData) error {
	trim := bytes.TrimSpace(data)
	switch {
	case bytes.HasPrefix(trim, []byte("mode:")):
		return parseGoCover(data, d)
	case bytes.HasPrefix(trim, []byte("{")):
		return parseCoveragePy(data, d)
	case bytes.HasPrefix(trim, []byte("<")):
		return parseCobertura(data, d)
	case bytes.Contains(data, []byte("\nSF:")) || bytes.HasPrefix(trim, []byte("SF:")) || bytes.HasPrefix(trim, []byte("TN:")):
		return parseLcov(data, d)
	}
	return errors.New("unrecognized coverage format in " + filepath.Base(p))
}

// Go: "example.com/mod/pkg/file.go:12.34,15.2 3 1" — paths are import paths.
func parseGoCover(data []byte, d *covData) error {
	mods := goModules()
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "mode:") {
			continue
		}
		colon := strings.LastIndex(line, ":")
		if colon < 0 {
			continue
		}
		fields := strings.Fields(line[colon+1:])
		if len(fields) != 3 {
			continue
		}
		start, end, ok := strings.Cut(fields[0], ",")
		if !ok {
			continue
		}
		sl, _ := strconv.Atoi(strings.SplitN(start, ".", 2)[0])
		el, _ := strconv.Atoi(strings.SplitN(end, ".", 2)[0])
		count, _ := strconv.Atoi(fields[2])
		file := goFile(line[:colon], mods)
		for l := sl; l <= el && l-sl < 10000; l++ {
			d.mark(file, l, count > 0)
		}
	}
	return sc.Err()
}

// goModules maps module paths to their directories in the repo, from every go.mod.
func goModules() map[string]string {
	mods := map[string]string{}
	out, _ := git("-C", repoRoot, "ls-files", "--", ":(glob)**/go.mod")
	for _, f := range strings.Fields(string(out)) {
		b, err := os.ReadFile(filepath.Join(repoRoot, f))
		if err != nil {
			continue
		}
		for _, l := range strings.Split(string(b), "\n") {
			if m, ok := strings.CutPrefix(strings.TrimSpace(l), "module "); ok {
				mods[strings.Trim(strings.TrimSpace(m), `"`)] = path.Dir(f)
				break
			}
		}
	}
	return mods
}

func goFile(imp string, mods map[string]string) string {
	best := ""
	for m := range mods {
		if (imp == m || strings.HasPrefix(imp, m+"/")) && len(m) > len(best) {
			best = m
		}
	}
	if best == "" {
		return imp // already relative, or an absolute path
	}
	return path.Join(mods[best], strings.TrimPrefix(strings.TrimPrefix(imp, best), "/"))
}

func parseLcov(data []byte, d *covData) error {
	file := ""
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		switch {
		case strings.HasPrefix(l, "SF:"):
			file = l[3:]
		case strings.HasPrefix(l, "DA:") && file != "":
			parts := strings.Split(l[3:], ",")
			if len(parts) >= 2 {
				ln, _ := strconv.Atoi(parts[0])
				n, _ := strconv.ParseFloat(parts[1], 64)
				d.mark(file, ln, n > 0)
			}
		case l == "end_of_record":
			file = ""
		}
	}
	return sc.Err()
}

func parseCoveragePy(data []byte, d *covData) error {
	var r struct {
		Files map[string]struct {
			Executed []int `json:"executed_lines"`
			Missing  []int `json:"missing_lines"`
		} `json:"files"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return err
	}
	if r.Files == nil {
		return errors.New("coverage JSON has no \"files\" (expected `coverage json` output)")
	}
	for f, v := range r.Files {
		for _, l := range v.Executed {
			d.mark(f, l, true)
		}
		for _, l := range v.Missing {
			d.mark(f, l, false)
		}
	}
	return nil
}

func parseCobertura(data []byte, d *covData) error {
	var r struct {
		Sources []string `xml:"sources>source"`
		Classes []struct {
			File  string `xml:"filename,attr"`
			Lines []struct {
				N    int     `xml:"number,attr"`
				Hits float64 `xml:"hits,attr"`
			} `xml:"lines>line"`
		} `xml:"packages>package>classes>class"`
	}
	if err := xml.Unmarshal(data, &r); err != nil {
		return err
	}
	prefix := ""
	for _, s := range r.Sources { // sources may be absolute dirs inside the repo
		if rel, err := filepath.Rel(repoRoot, s); err == nil && !strings.HasPrefix(rel, "..") && rel != "." {
			prefix = filepath.ToSlash(rel)
		}
	}
	for _, c := range r.Classes {
		f := c.File
		if prefix != "" && !filepath.IsAbs(f) {
			if _, err := os.Stat(filepath.Join(repoRoot, f)); err != nil {
				f = path.Join(prefix, f)
			}
		}
		for _, l := range c.Lines {
			d.mark(f, l.N, l.Hits > 0)
		}
	}
	return nil
}

type CovFile struct {
	Hit   []int `json:"hit"`
	Miss  []int `json:"miss"`
	Stale bool  `json:"stale,omitempty"` // file changed after the report was written
}

// coverageFor returns coverage for the given repo paths.
func coverageFor(paths []string) map[string]any {
	d := loadCoverage()
	if d == nil {
		return map[string]any{"found": false, "looked": covCandidates}
	}
	files := map[string]CovFile{}
	for _, p := range paths {
		m := d.Lines[p]
		if m == nil {
			continue
		}
		cf := CovFile{Hit: []int{}, Miss: []int{}}
		for l, hit := range m {
			if hit {
				cf.Hit = append(cf.Hit, l)
			} else {
				cf.Miss = append(cf.Miss, l)
			}
		}
		if st, err := os.Stat(filepath.Join(repoRoot, filepath.FromSlash(p))); err == nil && st.ModTime().After(d.Mod) {
			cf.Stale = true
		}
		files[p] = cf
	}
	return map[string]any{"found": true, "file": d.File, "at": d.Mod.UnixMilli(), "error": d.Err, "files": files, "total": len(d.Lines)}
}
