package main

// Session timeline and Claude agents.
//
// Timeline: every time the working tree changes, refdiff snapshots it as a git tree (via a
// scratch index, so the real index is never touched) and keeps the list in
// .git/refdiff/timeline.json. Any snapshot can be diffed against another or against the live
// working tree ("@wt"), which answers "what changed since I looked / since Claude started?".
//
// Agents: refdiff can run Claude Code headless (`claude -p`) in the repo, either to review the
// diff (it posts notes with `refdiff comments --add`, which appear live in the page) or to fix
// open notes (it edits files and resolves notes with `refdiff comments --resolve`). Their
// tool use streams into the page.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ---------- snapshots ----------

const wtSnap = "@wt" // pseudo-ref: a fresh snapshot of the working tree (tracked + untracked)

var snapMu sync.Mutex

// snapshotTree writes the current working tree (respecting .gitignore) as a tree object and
// returns its id. Uses a private index file seeded from the real one, so nothing visible changes.
func snapshotTree() (string, error) {
	snapMu.Lock()
	defer snapMu.Unlock()
	idx := filepath.Join(gitDir, "refdiff", "snap.index")
	if err := os.MkdirAll(filepath.Dir(idx), 0o755); err != nil {
		return "", err
	}
	if b, err := os.ReadFile(filepath.Join(gitDir, "index")); err == nil {
		os.WriteFile(idx, b, 0o644)
	} else {
		os.Remove(idx)
	}
	run := func(args ...string) (string, error) {
		cmd := exec.Command("git", append([]string{"-C", repoRoot}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_INDEX_FILE="+idx)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("git %s: %v: %s", args[0], err, strings.TrimSpace(string(out)))
		}
		return strings.TrimSpace(string(out)), nil
	}
	if _, err := run("add", "-A", "--ignore-errors"); err != nil {
		return "", err
	}
	return run("write-tree")
}

type Snap struct {
	Tree  string `json:"tree"`
	At    int64  `json:"at"`
	Label string `json:"label,omitempty"`
	Files int    `json:"files"`
	Add   int    `json:"add"`
	Del   int    `json:"del"`
}

var (
	tlMu     sync.Mutex
	timeline []Snap
	tlLoaded bool
	tlVer    atomic.Int64
)

func timelinePath() string { return filepath.Join(gitDir, "refdiff", "timeline.json") }

func loadTimeline() {
	if tlLoaded {
		return
	}
	tlLoaded = true
	if b, err := os.ReadFile(timelinePath()); err == nil {
		json.Unmarshal(b, &timeline)
	}
}

func saveTimeline() {
	b, _ := json.Marshal(timeline)
	tmp := timelinePath() + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		os.Rename(tmp, timelinePath())
	}
	tlVer.Add(1)
}

// recordSnapshot snapshots the working tree and appends it to the timeline unless it equals
// the latest entry (in which case a new label is attached to that entry instead).
func recordSnapshot(label string) (Snap, error) {
	tree, err := snapshotTree()
	if err != nil {
		return Snap{}, err
	}
	tlMu.Lock()
	defer tlMu.Unlock()
	loadTimeline()
	if n := len(timeline); n > 0 && timeline[n-1].Tree == tree {
		if label != "" && timeline[n-1].Label != label {
			if timeline[n-1].Label != "" {
				label = timeline[n-1].Label + " · " + label
			}
			timeline[n-1].Label = label
			saveTimeline()
		}
		return timeline[n-1], nil
	}
	s := Snap{Tree: tree, At: time.Now().UnixMilli(), Label: label}
	if n := len(timeline); n > 0 {
		if out, err := git("diff", "--shortstat", timeline[n-1].Tree, tree); err == nil {
			if m := shortstatRe.FindStringSubmatch(string(out)); m != nil {
				s.Files, _ = strconv.Atoi(m[1])
				s.Add, _ = strconv.Atoi(m[2])
				s.Del, _ = strconv.Atoi(m[3])
			}
		}
	}
	timeline = append(timeline, s)
	if len(timeline) > 300 {
		timeline = timeline[len(timeline)-300:]
	}
	saveTimeline()
	return s, nil
}

var snapReq = make(chan struct{}, 1)

// requestSnapshot asks the snapshot loop for an (unlabeled) snapshot; bursts coalesce.
func requestSnapshot() {
	select {
	case snapReq <- struct{}{}:
	default:
	}
}

func snapshotLoop() {
	recordSnapshot("refdiff started")
	for range snapReq {
		time.Sleep(1500 * time.Millisecond) // let a burst of edits settle
		select {
		case <-snapReq:
		default:
		}
		recordSnapshot("")
	}
}

// ---------- agents ----------

type AgentEvent struct {
	At   int64  `json:"at"`
	Kind string `json:"kind"` // tool | text | info | error
	Text string `json:"text"`
}

type Agent struct {
	ID      string       `json:"id"`
	Kind    string       `json:"kind"` // review | fix
	Title   string       `json:"title"`
	Status  string       `json:"status"` // running | done | failed | stopped
	Started int64        `json:"started"`
	Ended   int64        `json:"ended,omitempty"`
	Before  string       `json:"before,omitempty"` // snapshot tree before the run
	After   string       `json:"after,omitempty"`
	Summary string       `json:"summary,omitempty"`
	Cost    float64      `json:"cost,omitempty"`
	Notes   []string     `json:"notes,omitempty"` // note ids it targets (fix)
	Log     []AgentEvent `json:"log"`
	cancel  context.CancelFunc
}

var (
	agMu     sync.Mutex
	agents   []*Agent // newest last
	agentVer atomic.Int64
)

func (a *Agent) event(kind, text string) {
	agMu.Lock()
	a.Log = append(a.Log, AgentEvent{At: time.Now().UnixMilli(), Kind: kind, Text: text})
	if len(a.Log) > 500 {
		a.Log = a.Log[len(a.Log)-500:]
	}
	agMu.Unlock()
	agentVer.Add(1)
}

func agentsJSON() []byte {
	agMu.Lock()
	defer agMu.Unlock()
	b, _ := json.Marshal(agents)
	return b
}

func claudePath() (string, error) {
	if p, err := exec.LookPath("claude"); err == nil {
		return p, nil
	}
	home, _ := os.UserHomeDir()
	for _, p := range []string{filepath.Join(home, ".local/bin/claude"), filepath.Join(home, ".claude/local/claude"), "/opt/homebrew/bin/claude", "/usr/local/bin/claude"} {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	return "", errors.New("the `claude` CLI (Claude Code) was not found on PATH")
}

// diffCmd is the plain git command an agent can run to see the diff a query shows.
func diffCmd(q Query) string {
	parts := []string{"git", "diff"}
	if q.Staged {
		parts = append(parts, "--cached")
	}
	parts = append(parts, q.A)
	if q.B != "" && q.B != wtSnap && !q.Staged && !strings.Contains(q.A, "..") {
		parts = append(parts, q.B)
	}
	if len(q.Paths) > 0 {
		parts = append(append(parts, "--"), q.Paths...)
	}
	return strings.Join(parts, " ")
}

// optAgentAllow holds extra Claude Code permission rules from --agent-allow (e.g. "Bash(go test:*)").
var optAgentAllow []string

func shortDescribe(q Query) string {
	b := q.B
	switch {
	case q.Staged:
		b = "index"
	case strings.Contains(q.A, ".."):
		return q.A
	case b == "" || b == wtSnap:
		b = "working tree"
	}
	a := q.A
	if len(a) == 40 {
		a = a[:8]
	}
	return a + " → " + b
}

func describe(q Query) string {
	right := "the working tree (including uncommitted changes)"
	switch {
	case q.Staged:
		right = "the index (staged changes)"
	case strings.Contains(q.A, ".."):
		return "the range " + q.A
	case q.B != "" && q.B != wtSnap:
		right = q.B
	}
	return q.A + " → " + right
}

var shq = func(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

const reviewPrompt = `You are reviewing a code change in this git repository for the developer, who is watching your notes appear live in a diff viewer (refdiff).

The change: %s
See it with: %s
%s
Review it like a senior engineer: correctness bugs, edge cases, error handling, concurrency, security, data loss, API misuse, missing or wrong tests, and confusing code that will cause bugs later. Read surrounding code when you need context. Skip pure style nits and anything a formatter or linter would catch. Do not modify any files.

For each real issue, leave a note on the exact line(s), on the NEW version of the file, as soon as you find it:

  %s comments --add <path>:<line>[-<endline>] --author claude --severity <high|medium|low> -- '<note>'

- <path> is relative to the repository root; line numbers are in the new version of the file (for code that was removed, add --old and use old line numbers).
- A note is 1-3 sentences: what is wrong, why it matters, and the concrete fix. Use backticks for code.
- high = will break or corrupt something; medium = likely bug or real risk; low = worth fixing.
- Prefer a few well-founded notes over many speculative ones. If the change looks good, say so and add no notes.

When done, reply with a 2-4 sentence overall assessment.`

const fixPrompt = `The developer reviewed changes in this git repository and left these notes for you to address, in a diff viewer (refdiff) that shows your edits live:

%s

Address each note by editing the files in the working tree. Keep edits minimal and focused on what each note asks; match the surrounding style. Read code as needed for context.

After you address a note, mark it resolved right away (the developer sees it update):

  %s comments --resolve <id>

If a note should not be changed as asked (it is wrong, or needs the developer's decision), do not resolve it; instead leave a reply note next to it:

  %s comments --add <path>:<line> --author claude -- '<why>'

When done, reply with a short summary of what you changed, per note.`

// startAgent launches `claude -p` for kind "review" (of q's diff) or "fix" (of the given note ids).
func startAgent(kind string, q Query, ids []string) (*Agent, error) {
	agMu.Lock()
	for _, a := range agents {
		if a.Status == "running" {
			agMu.Unlock()
			return nil, errors.New("Claude is already working on: " + a.Title)
		}
	}
	agMu.Unlock()
	claude, err := claudePath()
	if err != nil {
		return nil, err
	}
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	self, _ = filepath.EvalSymlinks(self)
	if strings.ContainsAny(self, " '\"") {
		return nil, fmt.Errorf("refdiff's path %q contains spaces or quotes; move the binary to run agents", self)
	}

	a := &Agent{ID: newID(), Kind: kind, Status: "running", Started: time.Now().UnixMilli(), Log: []AgentEvent{}, Notes: ids}
	var prompt string
	tools := []string{"Read", "Grep", "Glob", "Bash(git diff:*)", "Bash(git log:*)", "Bash(git show:*)", "Bash(git status:*)",
		"Bash(" + self + " comments --add:*)"}
	tools = append(tools, optAgentAllow...)
	extra := []string{}
	switch kind {
	case "review":
		a.Title = "Review " + shortDescribe(q)
		untr := ""
		if q.untracked() {
			var names []string
			for _, f := range untrackedFiles(q.Paths) {
				names = append(names, f.New)
				if len(names) == 50 {
					break
				}
			}
			if len(names) > 0 {
				untr = "New untracked files (not in git diff; read them directly): " + strings.Join(names, ", ") + "\n"
			}
		}
		prompt = fmt.Sprintf(reviewPrompt, describe(q), diffCmd(q), untr, self)
	case "fix":
		cmu.Lock()
		all := loadComments()
		cmu.Unlock()
		var sel []*Comment
		want := map[string]bool{}
		for _, id := range ids {
			want[id] = true
		}
		for _, c := range all {
			if !c.Resolved && (want[c.ID] || want["all"]) {
				sel = append(sel, c)
			}
		}
		if len(sel) == 0 {
			return nil, errors.New("no open notes to fix")
		}
		a.Notes = nil
		for _, c := range sel {
			a.Notes = append(a.Notes, c.ID)
		}
		a.Title = fmt.Sprintf("Fix %d note%s", len(sel), map[bool]string{true: "", false: "s"}[len(sel) == 1])
		prompt = fmt.Sprintf(fixPrompt, commentsMarkdown(sel, false), self, self)
		// Only file edits are pre-approved: no acceptEdits mode, which would also auto-approve rm/mv.
		tools = append(tools, "Edit", "Write", "MultiEdit", "Bash("+self+" comments --resolve:*)")
	default:
		return nil, errors.New("unknown agent kind")
	}
	if s, err := recordSnapshot("before Claude: " + a.Title); err == nil {
		a.Before = s.Tree
	}

	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel
	args := append([]string{"-p", prompt, "--output-format", "stream-json", "--verbose", "--max-turns", "80",
		"--allowedTools", strings.Join(tools, ",")}, extra...)
	cmd := exec.CommandContext(ctx, claude, args...)
	cmd.Dir = repoRoot
	cmd.Stdin = nil
	for _, kv := range os.Environ() { // don't look like a nested Claude Code session
		if !strings.HasPrefix(kv, "CLAUDECODE=") && !strings.HasPrefix(kv, "CLAUDE_CODE_ENTRYPOINT=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}
	agMu.Lock()
	agents = append(agents, a)
	if len(agents) > 20 {
		agents = agents[len(agents)-20:]
	}
	agMu.Unlock()
	a.event("info", "Started Claude Code ("+a.Title+")")

	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 1<<20), 1<<26)
		gotResult := false
		for sc.Scan() {
			var ev map[string]any
			if json.Unmarshal(sc.Bytes(), &ev) != nil {
				continue
			}
			switch ev["type"] {
			case "assistant":
				msg, _ := ev["message"].(map[string]any)
				blocks, _ := msg["content"].([]any)
				for _, b := range blocks {
					bm, _ := b.(map[string]any)
					switch bm["type"] {
					case "tool_use":
						a.event("tool", toolLine(bm, self))
					case "text":
						if t, _ := bm["text"].(string); strings.TrimSpace(t) != "" {
							a.event("text", strings.TrimSpace(t))
						}
					}
				}
			case "result":
				gotResult = true
				agMu.Lock()
				a.Summary, _ = ev["result"].(string)
				a.Cost, _ = ev["total_cost_usd"].(float64)
				if isErr, _ := ev["is_error"].(bool); isErr {
					a.Status = "failed"
				}
				agMu.Unlock()
			}
		}
		err := cmd.Wait()
		agMu.Lock()
		switch {
		case ctx.Err() != nil:
			a.Status = "stopped"
		case err != nil && !gotResult:
			a.Status = "failed"
			if a.Summary == "" {
				a.Summary = strings.TrimSpace(stderr.String())
				if a.Summary == "" {
					a.Summary = err.Error()
				}
			}
		case a.Status == "running":
			a.Status = "done"
		}
		a.Ended = time.Now().UnixMilli()
		status := a.Status
		agMu.Unlock()
		if a.Kind == "fix" {
			if s, err := recordSnapshot("after Claude: " + a.Title); err == nil {
				agMu.Lock()
				a.After = s.Tree
				agMu.Unlock()
			}
		}
		a.event("info", "Finished: "+status)
	}()
	return a, nil
}

func stopAgent() bool {
	agMu.Lock()
	defer agMu.Unlock()
	for _, a := range agents {
		if a.Status == "running" && a.cancel != nil {
			a.cancel()
			return true
		}
	}
	return false
}

var homeRe = regexp.MustCompile(`^/(Users|home)/[^/]+`)

// toolLine renders a tool call as one short human line.
func toolLine(b map[string]any, self string) string {
	name, _ := b["name"].(string)
	in, _ := b["input"].(map[string]any)
	str := func(k string) string { s, _ := in[k].(string); return s }
	rel := func(p string) string {
		if r, err := filepath.Rel(repoRoot, p); err == nil && !strings.HasPrefix(r, "..") {
			return r
		}
		return homeRe.ReplaceAllString(p, "~")
	}
	switch name {
	case "Read":
		return "Reading " + rel(str("file_path"))
	case "Edit", "MultiEdit":
		return "Editing " + rel(str("file_path"))
	case "Write":
		return "Writing " + rel(str("file_path"))
	case "Grep":
		s := "Searching for " + str("pattern")
		if p := str("path"); p != "" {
			s += " in " + rel(p)
		}
		return s
	case "Glob":
		return "Finding files " + str("pattern")
	case "Bash":
		c := strings.ReplaceAll(str("command"), self, "refdiff")
		if i := strings.Index(c, "\n"); i > 0 {
			c = c[:i] + " …"
		}
		if len(c) > 160 {
			c = c[:160] + "…"
		}
		return "$ " + c
	case "TodoWrite":
		return "Planning"
	}
	return name
}

// ---------- refdiff comments --add ----------

// addComment implements `refdiff comments --add path:line[-end] [--old] [--rev R] [--author A] [--severity S] -- text`.
func addComment(spec, rev, author, severity string, old bool, text string) (*Comment, error) {
	path, rng, ok := strings.Cut(spec, ":")
	if !ok || path == "" {
		return nil, errors.New("expected <path>:<line>[-<end>]")
	}
	if filepath.IsAbs(path) {
		r, err := filepath.Rel(repoRoot, path)
		if err != nil || strings.HasPrefix(r, "..") {
			return nil, errors.New("path is outside the repository")
		}
		path = r
	}
	path = filepath.ToSlash(filepath.Clean(path))
	ls, le, _ := strings.Cut(rng, "-")
	line, err := strconv.Atoi(ls)
	if err != nil || line <= 0 {
		return nil, errors.New("bad line number")
	}
	end := line
	if le != "" {
		if end, err = strconv.Atoi(le); err != nil || end < line {
			return nil, errors.New("bad line range")
		}
	}
	side := "n"
	if old {
		side = "o"
		if rev == "" {
			rev = "HEAD"
		}
	}
	c := Comment{Path: path, Side: side, Line: line, End: end, Rev: rev, Text: text, Author: author, Severity: severity}
	if data, err := readAt(rev, path); err == nil {
		lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
		if line > len(lines) {
			return nil, fmt.Errorf("%s has only %d lines", path, len(lines))
		}
		c.Code = lines[line-1 : min(end, len(lines))]
	} else {
		return nil, err
	}
	return upsertComment(c)
}
