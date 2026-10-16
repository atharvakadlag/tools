// refdiff: live, browser-based diff viewer for git references.
//
//	refdiff                 working tree vs HEAD
//	refdiff A               working tree vs A
//	refdiff A B             A vs B
//	refdiff A..B / A...B    passed through to git diff
//	refdiff --staged [A]    index vs A (default HEAD)
//	refdiff A B -- paths    limit to paths
package main

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed index.html
var indexHTML []byte

//go:embed assets/hl.js
var hlJS []byte

type Hunk struct {
	OS int      `json:"os"`
	NS int      `json:"ns"`
	H  string   `json:"h"`
	L  []string `json:"l"`
}

type File struct {
	K     string  `json:"k"`
	Old   string  `json:"old"`
	New   string  `json:"new"`
	St    string  `json:"st"`
	Sim   string  `json:"sim,omitempty"`
	Mode  string  `json:"mode,omitempty"`
	Bin   bool    `json:"bin,omitempty"`
	Add   int     `json:"add"`
	Del   int     `json:"del"`
	Hunks []*Hunk `json:"hunks"`
}

type Query struct {
	A, B   string
	Staged bool
	Paths  []string
}

func (q Query) key() string {
	return fmt.Sprintf("%q|%q|%v|%q", q.A, q.B, q.Staged, q.Paths)
}

func (q Query) values() url.Values {
	v := url.Values{}
	v.Set("a", q.A)
	if q.B != "" {
		v.Set("b", q.B)
	}
	if q.Staged {
		v.Set("staged", "1")
	}
	for _, p := range q.Paths {
		v.Add("p", p)
	}
	return v
}

func queryFrom(v url.Values) (Query, error) {
	q := Query{A: v.Get("a"), B: v.Get("b"), Staged: v.Get("staged") == "1", Paths: v["p"]}
	if strings.HasPrefix(q.A, "-") || strings.HasPrefix(q.B, "-") {
		return q, errors.New("refs may not start with '-'")
	}
	return q, nil
}

// worktree reports whether the right side is the working tree or index (i.e. can change without commits).
func (q Query) worktree() bool { return q.Staged || (q.B == "" && !strings.Contains(q.A, "..")) }

var (
	optDir      = ""
	optContext  = 3
	optWS       = false
	optInterval = 700 * time.Millisecond
	repoRoot    string
	gitDir      string
)

func git(args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = optDir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return out, errors.New(msg)
		}
	}
	return out, err
}

func diffArgs(q Query) []string {
	args := []string{"-c", "core.quotePath=false", "diff", "--no-color", "--no-ext-diff", "-M",
		"--src-prefix=a/", "--dst-prefix=b/", "-U" + strconv.Itoa(optContext)}
	if optWS {
		args = append(args, "-w")
	}
	if q.Staged {
		args = append(args, "--cached")
	}
	if q.A != "" {
		args = append(args, q.A)
	}
	if q.B != "" && !q.Staged && !strings.Contains(q.A, "..") {
		args = append(args, q.B)
	}
	args = append(args, "--")
	return append(args, q.Paths...)
}

// ---------- parsing ----------

func unquote(s string) string {
	if strings.HasPrefix(s, `"`) {
		if u, err := strconv.Unquote(s); err == nil {
			return u
		}
	}
	return s
}

func stripPrefix(s, p string) string {
	s = unquote(s)
	if t, ok := strings.CutSuffix(s, "\t"); ok {
		s = t
	}
	return strings.TrimPrefix(s, p)
}

func splitGitPaths(s string) (string, string) {
	if n := len(s); n%2 == 1 {
		mid := n / 2
		if s[mid] == ' ' && strings.HasPrefix(s, "a/") && s[mid+1:mid+3] == "b/" && s[2:mid] == s[mid+3:] {
			return s[2:mid], s[mid+3:]
		}
	}
	if i := strings.LastIndex(s, " b/"); i > 0 {
		return stripPrefix(s[:i], "a/"), stripPrefix(s[i+1:], "b/")
	}
	return s, s
}

func parseRange(s string) (start, count int) {
	a, b, ok := strings.Cut(s, ",")
	start, _ = strconv.Atoi(a)
	count = 1
	if ok {
		count, _ = strconv.Atoi(b)
	}
	return
}

func parse(r io.Reader) ([]*File, uint64, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<30)
	all := fnv.New64a()
	files := []*File{}
	var f *File
	var fh interface {
		io.Writer
		Sum64() uint64
	}
	var h *Hunk
	oldRem, newRem := 0, 0
	finish := func() {
		if f != nil {
			f.K = strconv.FormatUint(fh.Sum64(), 36)
		}
	}
	for sc.Scan() {
		line := sc.Text()
		all.Write(sc.Bytes())
		all.Write([]byte{'\n'})
		if fh != nil {
			fh.Write(sc.Bytes())
			fh.Write([]byte{'\n'})
		}
		if h != nil && (oldRem > 0 || newRem > 0) {
			if line == "" {
				line = " "
			}
			switch line[0] {
			case '-':
				oldRem--
				f.Del++
			case '+':
				newRem--
				f.Add++
			case '\\':
			default:
				oldRem--
				newRem--
			}
			h.L = append(h.L, line)
			continue
		}
		if h != nil && strings.HasPrefix(line, `\`) {
			h.L = append(h.L, line)
			continue
		}
		switch {
		case strings.HasPrefix(line, "diff --git "):
			finish()
			f = &File{St: "M", Hunks: []*Hunk{}}
			fh = fnv.New64a()
			fh.Write(sc.Bytes())
			files = append(files, f)
			h = nil
			f.Old, f.New = splitGitPaths(line[len("diff --git "):])
		case f == nil:
		case strings.HasPrefix(line, "@@"):
			parts := strings.SplitN(line, " ", 4)
			if len(parts) < 3 {
				continue
			}
			os_, oc := parseRange(strings.TrimPrefix(parts[1], "-"))
			ns, nc := parseRange(strings.TrimPrefix(parts[2], "+"))
			h = &Hunk{OS: os_, NS: ns, H: line, L: []string{}}
			oldRem, newRem = oc, nc
			f.Hunks = append(f.Hunks, h)
		case h != nil:
		case strings.HasPrefix(line, "new file mode "):
			f.St = "A"
		case strings.HasPrefix(line, "deleted file mode "):
			f.St = "D"
		case strings.HasPrefix(line, "rename from "):
			f.St, f.Old = "R", unquote(line[len("rename from "):])
		case strings.HasPrefix(line, "rename to "):
			f.New = unquote(line[len("rename to "):])
		case strings.HasPrefix(line, "copy from "):
			f.St, f.Old = "C", unquote(line[len("copy from "):])
		case strings.HasPrefix(line, "copy to "):
			f.New = unquote(line[len("copy to "):])
		case strings.HasPrefix(line, "similarity index "):
			f.Sim = line[len("similarity index "):]
		case strings.HasPrefix(line, "old mode "):
			f.Mode = line[len("old mode "):] + f.Mode
		case strings.HasPrefix(line, "new mode "):
			f.Mode += " → " + line[len("new mode "):]
		case strings.HasPrefix(line, "Binary files "), strings.HasPrefix(line, "GIT binary patch"):
			f.Bin = true
		case strings.HasPrefix(line, "--- "):
			if p := line[4:]; p != "/dev/null" {
				f.Old = stripPrefix(p, "a/")
			}
		case strings.HasPrefix(line, "+++ "):
			if p := line[4:]; p != "/dev/null" {
				f.New = stripPrefix(p, "b/")
			}
		}
	}
	finish()
	return files, all.Sum64(), sc.Err()
}

func runDiff(q Query) ([]*File, uint64, error) {
	cmd := exec.Command("git", diffArgs(q)...)
	cmd.Dir = optDir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, 0, err
	}
	if err := cmd.Start(); err != nil {
		return nil, 0, err
	}
	files, sum, perr := parse(out)
	io.Copy(io.Discard, out)
	if err := cmd.Wait(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, 0, errors.New(msg)
		}
		return nil, 0, err
	}
	return files, sum, perr
}

// fingerprint is a cheap signature that changes whenever the diff might have changed.
func fingerprint(q Query) string {
	var b bytes.Buffer
	var refs []string
	for _, r := range []string{q.A, q.B} {
		if r != "" {
			refs = append(refs, r)
		}
	}
	if len(refs) > 0 {
		out, err := git(append([]string{"rev-parse"}, refs...)...)
		b.Write(out)
		if err != nil {
			b.WriteString(err.Error())
		}
	}
	if q.worktree() {
		if st, err := os.Stat(filepath.Join(gitDir, "index")); err == nil {
			fmt.Fprintf(&b, "|idx %d %d", st.ModTime().UnixNano(), st.Size())
		}
		out, _ := git(append([]string{"-c", "core.quotePath=false", "status", "--porcelain=v1", "-z", "--untracked-files=no", "--"}, q.Paths...)...)
		b.Write(out)
		if !q.Staged {
			for _, ent := range bytes.Split(out, []byte{0}) {
				p := string(ent)
				if len(p) > 3 && p[2] == ' ' {
					p = p[3:]
				}
				if st, err := os.Lstat(filepath.Join(repoRoot, p)); err == nil {
					fmt.Fprintf(&b, "|%d %d", st.ModTime().UnixNano(), st.Size())
				}
			}
		}
	}
	return b.String()
}

// ---------- watchers ----------

type watcher struct {
	q       Query
	mu      sync.Mutex
	fp      string
	hash    uint64
	ver     int
	payload []byte
	subs    map[chan int]struct{}
	lastSub time.Time
	ready   chan struct{}
}

var (
	wmu      sync.Mutex
	watchers = map[string]*watcher{}
)

func getWatcher(q Query) *watcher {
	wmu.Lock()
	defer wmu.Unlock()
	k := q.key()
	if w, ok := watchers[k]; ok {
		return w
	}
	w := &watcher{q: q, subs: map[chan int]struct{}{}, lastSub: time.Now(), ready: make(chan struct{})}
	watchers[k] = w
	go w.loop(k)
	return w
}

func (w *watcher) loop(k string) {
	w.refresh()
	close(w.ready)
	t := time.NewTicker(optInterval)
	defer t.Stop()
	for range t.C {
		w.mu.Lock()
		idle := len(w.subs) == 0 && time.Since(w.lastSub) > time.Minute
		w.mu.Unlock()
		if idle {
			wmu.Lock()
			delete(watchers, k)
			wmu.Unlock()
			return
		}
		w.refresh()
	}
}

func shortSha(ref string) string {
	if ref == "" || strings.Contains(ref, "..") {
		return ""
	}
	out, err := git("rev-parse", "--short", ref)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (w *watcher) refresh() {
	fp := fingerprint(w.q)
	w.mu.Lock()
	same := w.payload != nil && fp == w.fp
	w.mu.Unlock()
	if same {
		return
	}
	start := time.Now()
	files, sum, err := runDiff(w.q)
	ms := time.Since(start).Milliseconds()
	if err != nil {
		h := fnv.New64a()
		h.Write([]byte(err.Error()))
		sum = h.Sum64()
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.fp = fp
	if w.payload != nil && sum == w.hash {
		return
	}
	w.hash = sum
	w.ver++
	p := map[string]any{
		"ver": verStr(w.ver), "repo": filepath.Base(repoRoot), "a": w.q.A, "b": w.q.B, "staged": w.q.Staged,
		"paths": w.q.Paths, "aSha": shortSha(w.q.A), "bSha": shortSha(w.q.B), "ms": ms,
		"at": time.Now().UnixMilli(), "cmd": "git " + strings.Join(diffArgs(w.q)[2:], " "),
	}
	if err != nil {
		p["error"] = err.Error()
	} else {
		p["files"] = files
	}
	if _, _, ok := remoteBranch(w.q.A); ok {
		p["onto"] = w.q.A
	}
	if out, err := git("symbolic-ref", "--short", "-q", "HEAD"); err == nil {
		p["branch"] = strings.TrimSpace(string(out))
	}
	w.payload, _ = json.Marshal(p)
	for c := range w.subs {
		select {
		case c <- w.ver:
		default:
		}
	}
}

var bootID = strconv.FormatInt(time.Now().UnixNano(), 36)

func verStr(v int) string { return bootID + "-" + strconv.Itoa(v) }

// ---------- prefs (persisted server-side so they survive restarts/port changes) ----------

var prefsMu sync.Mutex

func prefsPath() string {
	d, err := os.UserConfigDir()
	if err != nil {
		d = os.TempDir()
	}
	return filepath.Join(d, "refdiff", "prefs.json")
}

func loadPrefs() map[string]any {
	m := map[string]any{}
	if b, err := os.ReadFile(prefsPath()); err == nil {
		json.Unmarshal(b, &m)
	}
	return m
}

func savePrefs(upd map[string]any) error {
	prefsMu.Lock()
	defer prefsMu.Unlock()
	m := loadPrefs()
	for k, v := range upd {
		m[k] = v
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	p := prefsPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// ---------- fetch & rebase ----------

// remoteBranch resolves a remote-tracking ref like origin/develop to (remote, branch, full ref).
func remoteBranch(ref string) (string, string, bool) {
	if ref == "" || strings.Contains(ref, "..") || strings.HasPrefix(ref, "-") {
		return "", "", false
	}
	out, err := git("rev-parse", "--symbolic-full-name", "--end-of-options", ref)
	if err != nil {
		return "", "", false
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n") // first line may echo "--end-of-options"
	rest, ok := strings.CutPrefix(lines[len(lines)-1], "refs/remotes/")
	if !ok {
		return "", "", false
	}
	remote, branch, ok := strings.Cut(rest, "/")
	return remote, branch, ok && branch != ""
}

func gitRun(timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = optDir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_EDITOR=true", "GIT_SEQUENCE_EDITOR=true")
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func rebaseInProgress() bool {
	for _, d := range []string{"rebase-merge", "rebase-apply"} {
		if _, err := os.Stat(filepath.Join(gitDir, d)); err == nil {
			return true
		}
	}
	return false
}

var opMu sync.Mutex

// fetchRebase runs `git fetch <remote> <branch>` then `git rebase --autostash <onto>`,
// aborting the rebase on conflicts so the branch is never left mid-rebase.
func fetchRebase(onto string) (bool, string) {
	if !opMu.TryLock() {
		return false, "Another fetch/rebase is already running."
	}
	defer opMu.Unlock()
	remote, branch, ok := remoteBranch(onto)
	if !ok {
		return false, onto + " is not a remote-tracking branch."
	}
	cur, err := git("symbolic-ref", "--short", "-q", "HEAD")
	if err != nil {
		return false, "HEAD is detached; check out a branch first."
	}
	if rebaseInProgress() {
		return false, "A rebase is already in progress in this repo."
	}
	var log strings.Builder
	out, err := gitRun(2*time.Minute, "fetch", remote, branch)
	fmt.Fprintf(&log, "$ git fetch %s %s\n%s\n", remote, branch, out)
	if err != nil {
		return false, "Fetch failed.\n\n" + log.String()
	}
	out, err = gitRun(5*time.Minute, "rebase", "--autostash", onto)
	fmt.Fprintf(&log, "\n$ git rebase --autostash %s\n%s\n", onto, out)
	if err != nil {
		if rebaseInProgress() {
			ab, _ := gitRun(time.Minute, "rebase", "--abort")
			fmt.Fprintf(&log, "\n$ git rebase --abort\n%s\n", ab)
			return false, fmt.Sprintf("Rebase of %s hit conflicts and was aborted — your branch is unchanged. Rebase manually to resolve.\n\n%s",
				strings.TrimSpace(string(cur)), log.String())
		}
		return false, "Rebase failed.\n\n" + log.String()
	}
	return true, log.String()
}

// ---------- http ----------

func serve(ln net.Listener) error {
	port := ln.Addr().(*net.TCPAddr).Port
	allowed := map[string]bool{
		fmt.Sprintf("127.0.0.1:%d", port): true,
		fmt.Sprintf("localhost:%d", port): true,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "text/html; charset=utf-8")
		rw.Header().Set("Cache-Control", "no-store")
		prefsMu.Lock()
		pj, _ := json.Marshal(loadPrefs()) // escapes <, > so it's safe inside <script>
		prefsMu.Unlock()
		rw.Write(bytes.Replace(indexHTML, []byte("/*PREFS*/{}"), pj, 1))
	})
	mux.HandleFunc("/api/prefs", func(rw http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("X-Refdiff") != "1" {
			http.Error(rw, "forbidden", 403)
			return
		}
		var upd map[string]any
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&upd); err != nil {
			http.Error(rw, err.Error(), 400)
			return
		}
		if err := savePrefs(upd); err != nil {
			http.Error(rw, err.Error(), 500)
			return
		}
		rw.WriteHeader(204)
	})
	mux.HandleFunc("/hl.js", func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		rw.Header().Set("Cache-Control", "max-age=86400")
		rw.Write(hlJS)
	})
	mux.HandleFunc("/api/diff", func(rw http.ResponseWriter, r *http.Request) {
		q, err := queryFrom(r.URL.Query())
		if err != nil {
			http.Error(rw, err.Error(), 400)
			return
		}
		w := getWatcher(q)
		<-w.ready
		w.mu.Lock()
		p := w.payload
		w.mu.Unlock()
		rw.Header().Set("Content-Type", "application/json")
		rw.Header().Set("Cache-Control", "no-store")
		rw.Write(p)
	})
	mux.HandleFunc("/api/events", func(rw http.ResponseWriter, r *http.Request) {
		q, err := queryFrom(r.URL.Query())
		if err != nil {
			http.Error(rw, err.Error(), 400)
			return
		}
		fl, ok := rw.(http.Flusher)
		if !ok {
			http.Error(rw, "no streaming", 500)
			return
		}
		w := getWatcher(q)
		c := make(chan int, 1)
		w.mu.Lock()
		w.subs[c] = struct{}{}
		w.mu.Unlock()
		defer func() {
			w.mu.Lock()
			delete(w.subs, c)
			w.lastSub = time.Now()
			w.mu.Unlock()
		}()
		<-w.ready
		rw.Header().Set("Content-Type", "text/event-stream")
		rw.Header().Set("Cache-Control", "no-store")
		w.mu.Lock()
		v := w.ver
		w.mu.Unlock()
		fmt.Fprintf(rw, "event: v\ndata: %s\n\n", verStr(v))
		fl.Flush()
		hb := time.NewTicker(15 * time.Second)
		defer hb.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case v := <-c:
				fmt.Fprintf(rw, "event: v\ndata: %s\n\n", verStr(v))
				fl.Flush()
			case <-hb.C:
				io.WriteString(rw, ": hb\n\n")
				fl.Flush()
			}
		}
	})
	mux.HandleFunc("/api/rebase", func(rw http.ResponseWriter, r *http.Request) {
		// Custom header forces a CORS preflight, so other sites can't trigger this.
		if r.Method != http.MethodPost || r.Header.Get("X-Refdiff") != "1" {
			http.Error(rw, "forbidden", 403)
			return
		}
		ok, msg := fetchRebase(r.URL.Query().Get("onto"))
		rw.Header().Set("Content-Type", "application/json")
		json.NewEncoder(rw).Encode(map[string]any{"ok": ok, "msg": msg})
	})
	mux.HandleFunc("/api/nav", func(rw http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		res, err := navigate(q.Get("name"), q.Get("rev"), q.Get("path"))
		if err != nil {
			http.Error(rw, err.Error(), 400)
			return
		}
		rw.Header().Set("Content-Type", "application/json")
		rw.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(rw).Encode(res)
	})
	mux.HandleFunc("/api/file", func(rw http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		data, err := readAt(q.Get("rev"), q.Get("path"))
		if err != nil {
			http.Error(rw, err.Error(), 404)
			return
		}
		if bytes.IndexByte(data[:min(len(data), 8000)], 0) >= 0 {
			http.Error(rw, "binary file", 415)
			return
		}
		rw.Header().Set("Content-Type", "text/plain; charset=utf-8")
		rw.Header().Set("Cache-Control", "no-store")
		rw.Write(data)
	})
	mux.HandleFunc("/api/refs", func(rw http.ResponseWriter, r *http.Request) {
		out, _ := git("for-each-ref", "--sort=-committerdate", "--count=2000",
			"--format=%(refname:short)%00%(refname)%00%(committerdate:relative)%00%(subject)",
			"refs/heads", "refs/remotes", "refs/tags")
		type ref struct {
			N string `json:"n"`
			K string `json:"k"`
			D string `json:"d,omitempty"`
			S string `json:"s,omitempty"`
		}
		refs := []ref{{N: "HEAD", K: "head"}}
		if out, err := git("log", "-1", "--format=%cr%x00%s"); err == nil {
			if d, s, ok := strings.Cut(strings.TrimSpace(string(out)), "\x00"); ok {
				refs[0].D, refs[0].S = d, s
			}
		}
		for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			f := strings.SplitN(l, "\x00", 4)
			if len(f) < 4 || (strings.HasPrefix(f[1], "refs/remotes/") && strings.HasSuffix(f[1], "/HEAD")) {
				continue
			}
			k := "branch"
			if strings.HasPrefix(f[1], "refs/remotes/") {
				k = "remote"
			} else if strings.HasPrefix(f[1], "refs/tags/") {
				k = "tag"
			}
			refs = append(refs, ref{N: f[0], K: k, D: f[2], S: f[3]})
		}
		rw.Header().Set("Content-Type", "application/json")
		rw.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(rw).Encode(refs)
	})
	return http.Serve(ln, http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if !allowed[r.Host] { // block DNS-rebinding / foreign origins
			http.Error(rw, "forbidden", 403)
			return
		}
		mux.ServeHTTP(rw, r)
	}))
}

func openBrowser(u string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	cmd.Start()
}

const usage = `refdiff - live browser diff between git references

usage: refdiff [options] [A [B]] [-- paths...]

  refdiff               working tree vs HEAD
  refdiff A             working tree vs A
  refdiff A B           A vs B          (also A..B, A...B)
  refdiff --staged [A]  index vs A (default HEAD)

options:
  -C <dir>         run in <dir>
  -U <n>           context lines (default 3)
  -w               ignore whitespace
  --staged         compare index instead of working tree
  --port <n>       listen port (default: stable per-repo port)
  --interval <ms>  poll interval for changes (default 700)
  --no-open        don't open the browser
`

func die(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "refdiff: "+format+"\n", a...)
	os.Exit(1)
}

func main() {
	var q Query
	var refs []string
	port, noOpen := 0, false
	args := os.Args[1:]
	next := func(i *int, name string) string {
		*i++
		if *i >= len(args) {
			die("%s needs a value", name)
		}
		return args[*i]
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "-h", "--help":
			fmt.Print(usage)
			return
		case "--":
			q.Paths = append(q.Paths, args[i+1:]...)
			i = len(args)
		case "-C":
			optDir = next(&i, a)
		case "-U":
			optContext, _ = strconv.Atoi(next(&i, a))
		case "-w":
			optWS = true
		case "--staged", "--cached":
			q.Staged = true
		case "--port":
			port, _ = strconv.Atoi(next(&i, a))
		case "--interval":
			ms, _ := strconv.Atoi(next(&i, a))
			optInterval = time.Duration(max(ms, 100)) * time.Millisecond
		case "--no-open":
			noOpen = true
		default:
			if strings.HasPrefix(a, "-") {
				die("unknown option %s (see --help)", a)
			}
			refs = append(refs, a)
		}
	}
	switch len(refs) {
	case 0:
		q.A = "HEAD"
	case 1:
		q.A = refs[0]
	case 2:
		q.A, q.B = refs[0], refs[1]
	default:
		die("too many refs (use -- before paths)")
	}

	out, err := git("rev-parse", "--show-toplevel", "--absolute-git-dir")
	if err != nil {
		die("not a git repository: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	repoRoot, gitDir = lines[0], lines[len(lines)-1]
	for _, r := range refs {
		if _, err := git("rev-parse", "--verify", "--quiet", "--end-of-options", r); err != nil && !strings.Contains(r, "..") {
			die("unknown ref %q", r)
		}
	}

	if port == 0 {
		h := fnv.New32a()
		h.Write([]byte(repoRoot))
		port = 30000 + int(h.Sum32()%10000)
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			fmt.Fprintf(os.Stderr, "refdiff: port %d busy (another refdiff for this repo?), using a random port\n", port)
			port = 0
		} else {
			ln.Close()
		}
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		die("%v", err)
	}
	go getWatcher(q) // warm up the first diff while the browser launches
	u := fmt.Sprintf("http://127.0.0.1:%d/?%s", ln.Addr().(*net.TCPAddr).Port, q.values().Encode())
	fmt.Printf("refdiff serving %s\n  %s\nCtrl-C to stop.\n", repoRoot, u)
	if !noOpen {
		openBrowser(u)
	}
	if err := serve(ln); err != nil {
		die("%v", err)
	}
}
