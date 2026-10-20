// mmdview: lightweight live-reloading Mermaid viewer.
//
//	mmdview [dir]        serve every .mmd / .mermaid / .md (```mermaid blocks) under dir
//	mmdview file.mmd     serve a single file
//
// The page re-renders whenever a file changes, and reports render results back so
// tools (e.g. Claude) can ask whether a diagram parsed: GET /api/status?path=...
package main

import (
	"bufio"
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// version is set at build time: -ldflags "-X main.version=..."
var version = "dev"

//go:embed index.html
var indexHTML []byte

//go:embed assets/mermaid.min.js
var mermaidJS []byte

const maxFiles = 2000

var skipDirs = map[string]bool{"node_modules": true, "vendor": true, "dist": true, "build": true, "target": true, "__pycache__": true}

type Diagram struct {
	Title string `json:"title"`
	Line  int    `json:"line"` // 1-based line of the first source line in the file
	Src   string `json:"src,omitempty"`
}

type File struct {
	Path     string    `json:"path"` // slash-separated, relative to root
	Rev      string    `json:"rev"`  // changes whenever the file does
	MTime    int64     `json:"mtime"`
	Diagrams []Diagram `json:"diagrams"`
	abs      string
	key      string // mtime+size, for the parse cache
}

type Result struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	Line  int    `json:"line,omitempty"` // file line of the error, when the parser reports one
}

type Report struct {
	Rev     string    `json:"rev"`
	Results []*Result `json:"results"`
	At      time.Time `json:"at"`
}

var (
	root     string // absolute dir being served
	single   string // absolute path when serving one file
	interval = 300 * time.Millisecond

	mu      sync.Mutex
	files   = map[string]*File{} // by Path
	ver     int
	subs    = map[chan string]struct{}{}
	reports = map[string]*Report{}                         // by Path
	changed = sync.NewCond(&mu)                            // broadcast on new files or reports
	bootID  = strconv.FormatInt(time.Now().UnixNano(), 36) // pages reload when the server restarts
)

func isDiagramFile(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".mmd", ".mermaid", ".md", ".markdown":
		return true
	}
	return false
}

func isMarkdown(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".md" || ext == ".markdown"
}

// parseFile extracts the diagrams in a file: the whole file for .mmd, each ```mermaid fence for markdown.
func parseFile(name string, data []byte) []Diagram {
	if !isMarkdown(name) {
		if len(bytes.TrimSpace(data)) == 0 {
			return []Diagram{{Title: "", Line: 1, Src: ""}}
		}
		return []Diagram{{Line: 1, Src: string(data)}}
	}
	var out []Diagram
	var heading string
	var cur *Diagram
	var buf []string
	fence := ""
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		t := strings.TrimSpace(line)
		if cur != nil {
			if strings.HasPrefix(t, fence) && strings.Trim(t, fence[:1]) == "" {
				cur.Src = strings.Join(buf, "\n")
				out = append(out, *cur)
				cur, buf, fence = nil, nil, ""
			} else {
				buf = append(buf, line)
			}
			continue
		}
		if fence != "" { // inside a non-mermaid fence
			if strings.HasPrefix(t, fence) && strings.Trim(t, fence[:1]) == "" {
				fence = ""
			}
			continue
		}
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			i := strings.IndexFunc(t, func(r rune) bool { return r != rune(t[0]) })
			if i < 0 {
				i = len(t)
			}
			fence = t[:i]
			if strings.EqualFold(strings.Fields(t[i:] + " x")[0], "mermaid") {
				cur = &Diagram{Title: heading, Line: n + 1}
			}
			continue
		}
		if strings.HasPrefix(t, "#") {
			heading = strings.TrimSpace(strings.TrimLeft(t, "#"))
		}
	}
	if cur != nil { // unterminated fence: take the rest
		cur.Src = strings.Join(buf, "\n")
		out = append(out, *cur)
	}
	return out
}

func walk() map[string]*File {
	found := map[string]*File{}
	add := func(abs string, info fs.FileInfo) {
		rel, err := filepath.Rel(root, abs)
		if err != nil {
			return
		}
		found[filepath.ToSlash(rel)] = &File{
			Path:  filepath.ToSlash(rel),
			MTime: info.ModTime().UnixMilli(),
			abs:   abs,
			key:   fmt.Sprintf("%d:%d", info.ModTime().UnixNano(), info.Size()),
		}
	}
	if single != "" {
		if info, err := os.Stat(single); err == nil {
			add(single, info)
		}
		return found
	}
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if p != root && (strings.HasPrefix(name, ".") || skipDirs[name]) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || !isDiagramFile(name) || len(found) >= maxFiles {
			return nil
		}
		if info, err := d.Info(); err == nil {
			add(p, info)
		}
		return nil
	})
	return found
}

// scan re-walks root, re-parses files whose mtime/size changed, and bumps the version if anything differs.
func scan() {
	found := walk()
	mu.Lock()
	old := files
	mu.Unlock()
	next := map[string]*File{}
	dirty := len(found) != len(old)
	for p, f := range found {
		if o, ok := old[p]; ok && o.key == f.key {
			next[p] = o
			continue
		}
		data, err := os.ReadFile(f.abs)
		if err != nil {
			continue
		}
		if isMarkdown(p) && !bytes.Contains(data, []byte("mermaid")) {
			f.Diagrams = nil
		} else {
			f.Diagrams = parseFile(p, data)
		}
		h := fnv.New64a()
		h.Write(data)
		f.Rev = strconv.FormatUint(h.Sum64(), 36)
		if o, ok := old[p]; ok && o.Rev == f.Rev {
			f.Rev = o.Rev // touched but identical
		} else {
			dirty = true
		}
		next[p] = f
	}
	for p := range old {
		if _, ok := next[p]; !ok {
			dirty = true
		}
	}
	mu.Lock()
	files = next
	if dirty {
		ver++
		for c := range subs {
			select {
			case c <- "v":
			default:
			}
		}
		changed.Broadcast()
	}
	mu.Unlock()
}

func watch() {
	for {
		time.Sleep(interval)
		scan()
	}
}

// listing returns the files that contain at least one diagram, newest first, without sources.
func listing() []File {
	mu.Lock()
	defer mu.Unlock()
	out := make([]File, 0, len(files))
	for _, f := range files {
		if len(f.Diagrams) == 0 {
			continue
		}
		c := *f
		c.Diagrams = make([]Diagram, len(f.Diagrams))
		for i, d := range f.Diagrams {
			c.Diagrams[i] = Diagram{Title: d.Title, Line: d.Line}
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].MTime != out[j].MTime {
			return out[i].MTime > out[j].MTime
		}
		return out[i].Path < out[j].Path
	})
	return out
}

// resolve maps a user-supplied path (relative to root, or absolute) to a known file key.
func resolve(p string) string {
	if p == "" {
		return ""
	}
	if filepath.IsAbs(p) {
		if rel, err := filepath.Rel(root, filepath.Clean(p)); err == nil {
			p = rel
		}
	}
	return filepath.ToSlash(filepath.Clean(p))
}

func writeJSON(rw http.ResponseWriter, v any) {
	rw.Header().Set("Content-Type", "application/json")
	rw.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(rw).Encode(v)
}

func broadcast(ev string) {
	mu.Lock()
	for c := range subs {
		select {
		case c <- ev:
		default:
		}
	}
	mu.Unlock()
}

func serve(ln net.Listener) error {
	port := ln.Addr().(*net.TCPAddr).Port
	allowed := map[string]bool{
		fmt.Sprintf("127.0.0.1:%d", port): true,
		fmt.Sprintf("localhost:%d", port): true,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(rw, r)
			return
		}
		rw.Header().Set("Content-Type", "text/html; charset=utf-8")
		rw.Header().Set("Cache-Control", "no-store")
		rw.Write(indexHTML)
	})
	mux.HandleFunc("/mermaid.min.js", func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		rw.Header().Set("Cache-Control", "max-age=86400")
		rw.Write(mermaidJS)
	})
	mux.HandleFunc("/api/files", func(rw http.ResponseWriter, r *http.Request) {
		l := listing()
		mu.Lock()
		v := ver
		mu.Unlock()
		writeJSON(rw, map[string]any{"root": root, "ver": v, "files": l})
	})
	mux.HandleFunc("/api/file", func(rw http.ResponseWriter, r *http.Request) {
		p := resolve(r.URL.Query().Get("path"))
		mu.Lock()
		f, ok := files[p]
		mu.Unlock()
		if !ok {
			http.Error(rw, "not found", 404)
			return
		}
		writeJSON(rw, f)
	})
	mux.HandleFunc("/api/events", func(rw http.ResponseWriter, r *http.Request) {
		fl, ok := rw.(http.Flusher)
		if !ok {
			http.Error(rw, "no streaming", 500)
			return
		}
		c := make(chan string, 8)
		mu.Lock()
		subs[c] = struct{}{}
		v := ver
		mu.Unlock()
		defer func() {
			mu.Lock()
			delete(subs, c)
			mu.Unlock()
		}()
		rw.Header().Set("Content-Type", "text/event-stream")
		rw.Header().Set("Cache-Control", "no-store")
		fmt.Fprintf(rw, "event: boot\ndata: %s\n\nevent: v\ndata: %d\n\n", bootID, v)
		fl.Flush()
		hb := time.NewTicker(15 * time.Second)
		defer hb.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case ev := <-c:
				if ev == "v" {
					mu.Lock()
					v = ver
					mu.Unlock()
					fmt.Fprintf(rw, "event: v\ndata: %d\n\n", v)
				} else { // "open:<path>" or "check:<path>"
					name, data, _ := strings.Cut(ev, ":")
					fmt.Fprintf(rw, "event: %s\ndata: %s\n\n", name, data)
				}
				fl.Flush()
			case <-hb.C:
				io.WriteString(rw, ": hb\n\n")
				fl.Flush()
			}
		}
	})
	// The page posts render results here; the custom header forces a CORS preflight so other sites can't.
	mux.HandleFunc("/api/report", func(rw http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("X-Mmdview") != "1" {
			http.Error(rw, "forbidden", 403)
			return
		}
		var body struct {
			Path    string    `json:"path"`
			Rev     string    `json:"rev"`
			Results []*Result `json:"results"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
			http.Error(rw, err.Error(), 400)
			return
		}
		mu.Lock()
		if f, ok := files[body.Path]; ok && f.Rev == body.Rev && len(body.Results) == len(f.Diagrams) {
			reports[body.Path] = &Report{Rev: body.Rev, Results: body.Results, At: time.Now()}
			changed.Broadcast()
		}
		mu.Unlock()
		rw.WriteHeader(204)
	})
	// Ask open pages to show a file: POST /api/open?path=...
	mux.HandleFunc("/api/open", func(rw http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(rw, "POST only", 405)
			return
		}
		scan() // pick up a just-written file immediately
		p := resolve(r.URL.Query().Get("path"))
		mu.Lock()
		_, ok := files[p]
		n := len(subs)
		mu.Unlock()
		if !ok {
			http.Error(rw, "not found: "+p, 404)
			return
		}
		broadcast("open:" + p)
		writeJSON(rw, map[string]any{"path": p, "viewers": n})
	})
	// GET /api/status?path=...&wait=5 — render result for the file's current contents (default: newest file).
	// Waits up to `wait` seconds for an open page to render it.
	mux.HandleFunc("/api/status", func(rw http.ResponseWriter, r *http.Request) {
		scan()
		p := resolve(r.URL.Query().Get("path"))
		if p == "" {
			if l := listing(); len(l) > 0 {
				p = l[0].Path
			}
		}
		mu.Lock()
		if f, ok := files[p]; ok && (reports[p] == nil || reports[p].Rev != f.Rev) {
			for c := range subs { // ask open pages to validate it
				select {
				case c <- "check:" + p:
				default:
				}
			}
		}
		mu.Unlock()
		wait, _ := strconv.ParseFloat(r.URL.Query().Get("wait"), 64)
		deadline := time.Now().Add(time.Duration(min(max(wait, 0), 60) * float64(time.Second)))
		// wake the cond periodically so the deadline is honoured
		stop := make(chan struct{})
		defer close(stop)
		go func() {
			t := time.NewTicker(100 * time.Millisecond)
			defer t.Stop()
			for {
				select {
				case <-stop:
					return
				case <-t.C:
					mu.Lock()
					changed.Broadcast()
					mu.Unlock()
				}
			}
		}()
		mu.Lock()
		defer mu.Unlock()
		for {
			f, ok := files[p]
			rep := reports[p]
			fresh := ok && rep != nil && rep.Rev == f.Rev
			if fresh || time.Now().After(deadline) || r.Context().Err() != nil {
				out := map[string]any{"path": p, "exists": ok, "viewers": len(subs), "rendered": fresh}
				if ok {
					out["rev"] = f.Rev
					res := make([]map[string]any, len(f.Diagrams))
					allOK := fresh
					for i, d := range f.Diagrams {
						m := map[string]any{"index": i, "line": d.Line, "title": d.Title}
						if fresh {
							m["ok"] = rep.Results[i].OK
							if !rep.Results[i].OK {
								allOK = false
								m["error"] = rep.Results[i].Error
								if rep.Results[i].Line > 0 {
									m["error_line"] = rep.Results[i].Line
								}
							}
						}
						res[i] = m
					}
					out["diagrams"] = res
					out["ok"] = allOK
					if !fresh {
						if len(subs) == 0 {
							out["hint"] = "no browser has the viewer open, so nothing rendered it"
						} else {
							out["hint"] = "timed out waiting for the viewer to render this revision"
						}
					}
				}
				writeJSON(rw, out)
				return
			}
			changed.Wait()
		}
	})
	return http.Serve(ln, http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		// Reject DNS-rebinding: only answer to our own host names.
		if !allowed[r.Host] {
			http.Error(rw, "forbidden host", 403)
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

const usage = `mmdview - live-reloading Mermaid viewer

usage: mmdview [options] [dir | file]

  mmdview              serve diagrams under the current directory
  mmdview docs/        serve .mmd, .mermaid and markdown mermaid blocks under docs/
  mmdview flow.mmd     serve a single file

options:
  --port <n>       listen port (default: stable per-directory port)
  --interval <ms>  poll interval for changes (default 300)
  --no-open        don't open the browser
  --version        print version

API (for scripts):
  GET  /api/status?path=<file>&wait=<s>   render result of the file's current contents
  POST /api/open?path=<file>              switch open pages to <file>
`

func die(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "mmdview: "+format+"\n", a...)
	os.Exit(1)
}

func main() {
	port, noOpen, target := 0, false, "."
	args := os.Args[1:]
	next := func(i *int, name string) string {
		*i++
		if *i >= len(args) {
			die("%s needs a value", name)
		}
		return args[*i]
	}
	var pos []string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; a {
		case "-h", "--help":
			fmt.Print(usage)
			return
		case "--version":
			fmt.Println("mmdview", version)
			return
		case "--port":
			port, _ = strconv.Atoi(next(&i, a))
		case "--interval":
			ms, _ := strconv.Atoi(next(&i, a))
			interval = time.Duration(max(ms, 50)) * time.Millisecond
		case "--no-open":
			noOpen = true
		default:
			if strings.HasPrefix(a, "-") {
				die("unknown option %s (see --help)", a)
			}
			pos = append(pos, a)
		}
	}
	if len(pos) > 1 {
		die("expected one dir or file")
	} else if len(pos) == 1 {
		target = pos[0]
	}
	abs, err := filepath.Abs(target)
	if err != nil {
		die("%v", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		die("%v", err)
	}
	root = abs
	if !info.IsDir() {
		single, root = abs, filepath.Dir(abs)
	}
	scan()
	go watch()

	if port == 0 {
		h := fnv.New32a()
		h.Write([]byte(abs))
		port = 40000 + int(h.Sum32()%10000)
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			fmt.Fprintf(os.Stderr, "mmdview: port %d busy, using a random port\n", port)
			port = 0
		} else {
			ln.Close()
		}
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		die("%v", err)
	}
	u := fmt.Sprintf("http://127.0.0.1:%d/", ln.Addr().(*net.TCPAddr).Port)
	fmt.Printf("mmdview serving %s\n  %s\nCtrl-C to stop.\n", abs, u)
	if !noOpen {
		openBrowser(u)
	}
	if err := serve(ln); err != nil {
		die("%v", err)
	}
}
