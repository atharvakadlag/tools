package main

// Review layer: commit timeline for the compared range, and line comments that persist per
// worktree (in .git/refdiff/comments.json) so they survive restarts and can be read back by
// `refdiff comments` — e.g. by Claude Code, to address them.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---------- commits ----------

type Commit struct {
	Sha     string `json:"sha"`
	Short   string `json:"short"`
	Parent  string `json:"parent"` // first parent, or the empty tree for a root commit
	Merge   bool   `json:"merge,omitempty"`
	Author  string `json:"author"`
	At      int64  `json:"at"`
	Subject string `json:"subject"`
	Body    string `json:"body,omitempty"`
	Files   int    `json:"files"`
	Add     int    `json:"add"`
	Del     int    `json:"del"`
}

const emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

var shortstatRe = regexp.MustCompile(`(\d+) files? changed(?:, (\d+) insertions?\(\+\))?(?:, (\d+) deletions?\(-\))?`)

// commitRange returns the commits a query covers (newest first), or, when it covers none
// (e.g. working tree vs HEAD), the recent history of HEAD with recent=true.
func commitRange(q Query) (list []Commit, rng string, recent bool, err error) {
	switch {
	case strings.Contains(q.A, ".."):
		rng = q.A
	case q.Staged || q.B == "":
		rng = q.A + "..HEAD"
	default:
		rng = q.A + ".." + q.B
	}
	if strings.HasPrefix(rng, "-") {
		return nil, rng, false, errors.New("bad range")
	}
	list, err = gitLog(rng, 500)
	if err == nil && len(list) == 0 && !strings.Contains(q.A, "..") && (q.B == "" || q.Staged) {
		list, err = gitLog("HEAD", 30)
		recent = true
	}
	return list, rng, recent, err
}

func gitLog(rev string, max int) ([]Commit, error) {
	out, err := git("log", "--shortstat", "--max-count="+strconv.Itoa(max),
		"--format=%x1e%H%x00%h%x00%P%x00%an%x00%at%x00%s%x00%b%x00", "--end-of-options", rev, "--")
	if err != nil {
		return nil, err
	}
	list := []Commit{}
	for _, rec := range strings.Split(string(out), "\x1e") {
		f := strings.Split(rec, "\x00")
		if len(f) < 8 {
			continue
		}
		c := Commit{Sha: f[0], Short: f[1], Author: f[3], Subject: f[5], Body: strings.TrimSpace(f[6])}
		c.At, _ = strconv.ParseInt(f[4], 10, 64)
		parents := strings.Fields(f[2])
		c.Parent, c.Merge = emptyTree, len(parents) > 1
		if len(parents) > 0 {
			c.Parent = parents[0]
		}
		if m := shortstatRe.FindStringSubmatch(f[7]); m != nil {
			c.Files, _ = strconv.Atoi(m[1])
			c.Add, _ = strconv.Atoi(m[2])
			c.Del, _ = strconv.Atoi(m[3])
		}
		list = append(list, c)
	}
	return list, nil
}

// ---------- comments ----------

type Comment struct {
	ID       string   `json:"id"`
	Path     string   `json:"path"`
	Side     string   `json:"side"` // "n" (new/right) or "o" (old/left)
	Line     int      `json:"line"`
	End      int      `json:"end"`
	Rev      string   `json:"rev"`  // revision the line numbers refer to ("" = working tree)
	Code     []string `json:"code"` // the commented lines, used to re-anchor after edits
	Text     string   `json:"text"`
	Branch   string   `json:"branch,omitempty"`
	Created  int64    `json:"created"`
	Updated  int64    `json:"updated"`
	Resolved bool     `json:"resolved,omitempty"`
}

var cmu sync.Mutex

func commentsPath() string { return filepath.Join(gitDir, "refdiff", "comments.json") }

func loadComments() []*Comment {
	list := []*Comment{}
	if b, err := os.ReadFile(commentsPath()); err == nil {
		json.Unmarshal(b, &list)
	}
	return list
}

func saveComments(list []*Comment) error {
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.Line < b.Line
	})
	b, _ := json.MarshalIndent(list, "", "  ")
	p := commentsPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func commentsStamp() string {
	st, err := os.Stat(commentsPath())
	if err != nil {
		return "0"
	}
	return strconv.FormatInt(st.ModTime().UnixNano(), 36) + "." + strconv.FormatInt(st.Size(), 36)
}

func newID() string {
	b := make([]byte, 4)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// upsertComment creates (empty ID) or updates a comment.
func upsertComment(c Comment) (*Comment, error) {
	c.Text = strings.TrimRight(c.Text, " \t\r\n")
	if c.Text == "" || c.Path == "" || c.Line <= 0 {
		return nil, errors.New("comment needs path, line and text")
	}
	if c.Side != "o" {
		c.Side = "n"
	}
	if c.End < c.Line {
		c.End = c.Line
	}
	if len(c.Code) > 200 {
		c.Code = c.Code[:200]
	}
	cmu.Lock()
	defer cmu.Unlock()
	list := loadComments()
	now := time.Now().UnixMilli()
	if c.ID != "" {
		for _, x := range list {
			if x.ID == c.ID {
				x.Text, x.Resolved, x.Updated = c.Text, c.Resolved, now
				if c.Line > 0 && len(c.Code) > 0 { // re-anchored by the client
					x.Line, x.End, x.Code = c.Line, c.End, c.Code
				}
				return x, saveComments(list)
			}
		}
		return nil, errors.New("no such comment")
	}
	c.ID, c.Created, c.Updated = newID(), now, now
	if out, err := git("symbolic-ref", "--short", "-q", "HEAD"); err == nil {
		c.Branch = strings.TrimSpace(string(out))
	}
	list = append(list, &c)
	return &c, saveComments(list)
}

// editComments applies fn to the list under the lock and saves; fn returns how many it touched.
func editComments(fn func([]*Comment) ([]*Comment, int)) (int, error) {
	cmu.Lock()
	defer cmu.Unlock()
	list, n := fn(loadComments())
	if n == 0 {
		return 0, nil
	}
	return n, saveComments(list)
}

func deleteComments(match func(*Comment) bool) (int, error) {
	return editComments(func(list []*Comment) ([]*Comment, int) {
		keep := list[:0]
		for _, c := range list {
			if !match(c) {
				keep = append(keep, c)
			}
		}
		return keep, len(list) - len(keep)
	})
}

func resolveComments(ids []string, resolved bool) (int, error) {
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	return editComments(func(list []*Comment) ([]*Comment, int) {
		n := 0
		for _, c := range list {
			if want[c.ID] || want["all"] {
				c.Resolved, c.Updated = resolved, time.Now().UnixMilli()
				n++
			}
		}
		return list, n
	})
}

// anchor finds where a comment's code now lives in its file (at its revision), searching
// outward from the original line. Returns the current start line, or 0 if the code is gone.
func anchor(c *Comment) int {
	if len(c.Code) == 0 {
		return c.Line
	}
	data, err := readAt(c.Rev, c.Path)
	if err != nil {
		return 0
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	match := func(i int) bool { // i is 0-based start
		if i < 0 || i+len(c.Code) > len(lines) {
			return false
		}
		for j, l := range c.Code {
			if lines[i+j] != l {
				return false
			}
		}
		return true
	}
	for d := 0; d < len(lines); d++ {
		if match(c.Line - 1 - d) {
			return c.Line - d
		}
		if d > 0 && match(c.Line-1+d) {
			return c.Line + d
		}
	}
	return 0
}

var mdLang = map[string]string{".go": "go", ".py": "python", ".js": "js", ".jsx": "jsx", ".ts": "ts", ".tsx": "tsx",
	".java": "java", ".rs": "rust", ".rb": "ruby", ".c": "c", ".h": "c", ".cpp": "cpp", ".cc": "cpp", ".cs": "csharp",
	".scala": "scala", ".sh": "bash", ".yml": "yaml", ".yaml": "yaml", ".json": "json", ".sql": "sql", ".md": "markdown",
	".html": "html", ".css": "css", ".kt": "kotlin", ".swift": "swift", ".proto": "protobuf", ".tf": "hcl"}

// commentsMarkdown renders comments as a prompt an agent can act on.
func commentsMarkdown(list []*Comment, all bool) string {
	var open []*Comment
	for _, c := range list {
		if all || !c.Resolved {
			open = append(open, c)
		}
	}
	if len(open) == 0 {
		if all {
			return "No review comments.\n"
		}
		return "No open review comments.\n"
	}
	var b strings.Builder
	head := filepath.Base(repoRoot)
	if out, err := git("symbolic-ref", "--short", "-q", "HEAD"); err == nil {
		head += " (" + strings.TrimSpace(string(out)) + ")"
	}
	fmt.Fprintf(&b, "# Code review comments — %s\n\n", head)
	fmt.Fprintf(&b, "%d comment%s left in refdiff. Each one quotes the code it is about; line numbers are where that code is now.\n",
		len(open), map[bool]string{true: "", false: "s"}[len(open) == 1])
	fmt.Fprintf(&b, "Address each one, then mark it done with `refdiff comments --resolve <id>` (run in the repo).\n")
	for i, c := range open {
		line, end, note := c.Line, c.End, ""
		if n := anchor(c); n == 0 {
			note = " — the quoted code has since changed or moved"
		} else {
			line, end = n, n+(c.End-c.Line)
		}
		loc := c.Path + ":" + strconv.Itoa(line)
		if end > line {
			loc += "-" + strconv.Itoa(end)
		}
		where := "working tree"
		if c.Rev != "" {
			where = c.Rev
		}
		if c.Side == "o" {
			where += ", old/removed side"
		}
		status := ""
		if c.Resolved {
			status = " [resolved]"
		}
		fmt.Fprintf(&b, "\n## %d. `%s` (id `%s`, %s%s)%s\n\n", i+1, loc, c.ID, where, note, status)
		if len(c.Code) > 0 {
			fmt.Fprintf(&b, "```%s\n%s\n```\n\n", mdLang[strings.ToLower(filepath.Ext(c.Path))], strings.Join(c.Code, "\n"))
		}
		b.WriteString(c.Text + "\n")
	}
	return b.String()
}

const commentsUsage = `usage: refdiff comments [options]

Print the review comments left in the refdiff page for this repo (open ones, as markdown).

  --all              include resolved comments
  --json             print JSON instead of markdown
  --resolve <id>...  mark comments resolved ("all" for every comment)
  --reopen <id>...   mark comments unresolved
  --delete <id>...   delete comments
  --clear            delete all resolved comments
  -C <dir>           run in <dir>
`

func commentsCmd(args []string) {
	all, asJSON := false, false
	var action string
	var ids []string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; a {
		case "-h", "--help":
			fmt.Print(commentsUsage)
			return
		case "--all":
			all = true
		case "--json":
			asJSON = true
		case "--clear":
			action = a
		case "--resolve", "--reopen", "--delete":
			action = a
		case "-C":
			if i++; i >= len(args) {
				die("-C needs a value")
			}
			optDir = args[i]
		default:
			if action == "" || strings.HasPrefix(a, "-") {
				die("comments: unexpected %s (see refdiff comments --help)", a)
			}
			ids = append(ids, a)
		}
	}
	findRepo()
	if len(ids) == 0 && (action == "--resolve" || action == "--reopen" || action == "--delete") {
		die("%s needs comment ids", action)
	}
	var n int
	var err error
	switch action {
	case "":
		list := loadComments()
		if asJSON {
			if !all {
				open := []*Comment{}
				for _, c := range list {
					if !c.Resolved {
						open = append(open, c)
					}
				}
				list = open
			}
			for _, c := range list {
				if l := anchor(c); l > 0 {
					c.End, c.Line = l+(c.End-c.Line), l
				}
			}
			b, _ := json.MarshalIndent(list, "", "  ")
			fmt.Println(string(b))
		} else {
			fmt.Print(commentsMarkdown(list, all))
		}
		return
	case "--clear":
		n, err = deleteComments(func(c *Comment) bool { return c.Resolved })
	case "--delete":
		set := map[string]bool{}
		for _, id := range ids {
			set[id] = true
		}
		n, err = deleteComments(func(c *Comment) bool { return set[c.ID] })
	default:
		n, err = resolveComments(ids, action == "--resolve")
	}
	if err != nil {
		die("%v", err)
	}
	fmt.Printf("%s: %d comment%s\n", strings.TrimPrefix(action, "--"), n, map[bool]string{true: "", false: "s"}[n == 1])
}
