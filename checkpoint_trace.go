package main

// checkpoint_trace.go: traceability of file changes (prefix "ob"/"cp").
//
// The shadow repository of checkpoint.go only kept trees. Here every
// checkpoint that changed the tree also becomes a commit on
// refs/jazz/trace whose message carries who made it:
//
//	jazz checkpoint: step
//
//	Jazz-Session: 251020-101500-ab12
//	Jazz-Step: 7
//	Jazz-Model: kilo/poolside/laguna-s-2.1:free
//	Jazz-Prompt-Sha256: 3f2a…
//	Jazz-Reason: step
//
// The turn-start snapshot is committed too (reason turn-start), so changes
// made by hand between turns are attributed to "fora do jazz". `jazz
// changes <file>` (alias `jazz blame`) walks this history for one path.
// Commits use a fixed identity and never touch the project's own .git.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const cpTraceRef = "refs/jazz/trace"

// obTraceCtx says who is driving the toolbox right now.
type obTraceCtx struct {
	mu        sync.Mutex
	sid       string
	model     string
	promptSHA string
	step      int
}

func (t *Toolbox) obSetTrace(sid, model, promptSHA string) {
	t.obTrace.mu.Lock()
	t.obTrace.sid, t.obTrace.model, t.obTrace.promptSHA, t.obTrace.step = sid, model, promptSHA, 0
	t.obTrace.mu.Unlock()
}

func (t *Toolbox) obSetStep(step int, model string) {
	t.obTrace.mu.Lock()
	t.obTrace.step = step
	if model != "" {
		t.obTrace.model = model
	}
	t.obTrace.mu.Unlock()
}

func (t *Toolbox) obTraceMsg(reason string) string {
	t.obTrace.mu.Lock()
	defer t.obTrace.mu.Unlock()
	var b strings.Builder
	fmt.Fprintf(&b, "jazz checkpoint: %s\n\n", reason)
	fmt.Fprintf(&b, "Jazz-Session: %s\nJazz-Step: %d\nJazz-Model: %s\nJazz-Prompt-Sha256: %s\nJazz-Reason: %s\nJazz-Time: %s\n",
		orDash(t.obTrace.sid), t.obTrace.step, orDash(t.obTrace.model), orDash(t.obTrace.promptSHA), reason, time.Now().UTC().Format(time.RFC3339))
	return b.String()
}

// obCheckpoint snapshots the tree and commits it with the trace message.
// It returns the tree id ("" when checkpoints are off or failed).
func (t *Toolbox) obCheckpoint(reason string) string {
	c := t.checkpoints()
	if c == nil {
		return ""
	}
	tree, err := c.Snapshot()
	if err != nil || tree == "" {
		return ""
	}
	t.obCommit(c, tree, reason)
	// the next bash diff starts here, so write/edit changes of this step are
	// not reported (and audited) again as bash changes
	t.mu.Lock()
	if n := len(t.turns); n > 0 && t.turns[n-1].cpLast != "" {
		t.turns[n-1].cpLast = tree
	}
	t.mu.Unlock()
	return tree
}

// obCommit records tree on the trace ref and emits the checkpoint event.
func (t *Toolbox) obCommit(c *cpStore, tree, reason string) {
	commit, changed, err := c.Commit(tree, t.obTraceMsg(reason))
	f := map[string]any{"tree": tree, "reason": reason, "changed": changed}
	if commit != "" {
		f["commit"] = commit
	}
	if err != nil {
		f["error"] = oneLine(err.Error(), 200)
	}
	obEmit("checkpoint", f)
}

// Commit adds tree on top of the trace ref (no commit when the tree did not
// change). changed reports whether a new commit was made.
func (c *cpStore) Commit(tree, msg string) (commit string, changed bool, err error) {
	if tree == "" {
		return "", false, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.initLocked(); err != nil {
		return "", false, err
	}
	parent := ""
	if out, err := c.cmd(nil, nil, "rev-parse", "-q", "--verify", cpTraceRef); err == nil {
		parent = strings.TrimSpace(string(out))
	}
	if parent != "" {
		if out, err := c.cmd(nil, nil, "rev-parse", parent+"^{tree}"); err == nil && strings.TrimSpace(string(out)) == tree {
			return parent, false, nil
		}
	}
	args := []string{"-c", "commit.gpgsign=false", "commit-tree", tree}
	if parent != "" {
		args = append(args, "-p", parent)
	}
	ctx, cancel := context.WithTimeout(context.Background(), cpTimeout)
	defer cancel()
	cmd := c.gitCmd(ctx, args...)
	cmd.Env = append(cmd.Env, "GIT_AUTHOR_NAME=jazz", "GIT_AUTHOR_EMAIL=user@localhost", "GIT_COMMITTER_NAME=jazz", "GIT_COMMITTER_EMAIL=user@localhost")
	cmd.Stdin = strings.NewReader(msg)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", false, fmt.Errorf("git commit-tree: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	commit = strings.TrimSpace(string(out))
	upd := []string{"update-ref", cpTraceRef, commit}
	if parent != "" {
		upd = append(upd, parent) // compare-and-swap: two jazz processes never lose a commit silently
	}
	if _, err := c.cmd(nil, nil, upd...); err != nil {
		return commit, true, err
	}
	return commit, true, nil
}

// cpChange is one path that differs between two trees.
type cpChange struct {
	Path     string
	Status   string // A, M, D, T
	Old, New string // git blob ids ("" for a missing side)
}

// ChangedDetail is Changed with blob ids and status (git diff-tree --raw).
func (c *cpStore) ChangedDetail(a, b string) ([]cpChange, error) {
	if a == "" || b == "" || a == b {
		return nil, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.initLocked(); err != nil {
		return nil, err
	}
	out, err := c.cmd(nil, nil, "diff-tree", "-r", "-z", "--no-renames", a, b)
	if err != nil {
		return nil, err
	}
	return cpParseRaw(out), nil
}

// cpParseRaw parses `diff-tree -r -z` output: ":m1 m2 old new S\0path\0".
func cpParseRaw(out []byte) []cpChange {
	parts := bytes.Split(out, []byte{0})
	var res []cpChange
	for i := 0; i+1 < len(parts); i += 2 {
		head := strings.Fields(strings.TrimPrefix(string(parts[i]), ":"))
		if len(head) < 5 {
			break
		}
		ch := cpChange{Path: string(parts[i+1]), Old: head[2], New: head[3], Status: head[4][:1]}
		if strings.Trim(ch.Old, "0") == "" {
			ch.Old = ""
		}
		if strings.Trim(ch.New, "0") == "" {
			ch.New = ""
		}
		res = append(res, ch)
	}
	return res
}

// cpAuditChanges writes one audit record per path a bash call changed.
func (t *Toolbox) cpAuditChanges(changes []cpChange) {
	for _, ch := range changes {
		op := map[string]string{"A": "create", "M": "edit", "D": "delete", "T": "type"}[ch.Status]
		f := map[string]any{"path": filepath.Join(t.Root, filepath.FromSlash(ch.Path)), "op": op, "via": "bash"}
		if ch.Old != "" {
			f["before"] = "git:" + ch.Old
		}
		if ch.New != "" {
			f["after"] = "git:" + ch.New
		}
		auditRec("file.change", f)
	}
}

// obChange is one commit of the trace history that touched a path.
type obChange struct {
	Commit, Session, Model, Prompt, Reason string
	Step                                   int
	Time                                   time.Time
	Added, Deleted                         int
	Binary                                 bool
}

// TraceLog lists the trace commits that changed rel (newest first).
func (c *cpStore) TraceLog(rel string, limit int) ([]obChange, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.initLocked(); err != nil {
		return nil, err
	}
	if _, err := c.cmd(nil, nil, "rev-parse", "-q", "--verify", cpTraceRef); err != nil {
		return nil, errors.New("nenhum checkpoint rastreado ainda nesta pasta")
	}
	args := []string{"log", "--format=%x1e%H%x1f%ct%x1f%B%x1f", "--numstat", "-n", strconv.Itoa(limit), cpTraceRef, "--", rel}
	out, err := c.cmd(nil, nil, args...)
	if err != nil {
		return nil, err
	}
	var res []obChange
	for _, rec := range strings.Split(string(out), "\x1e") {
		if strings.TrimSpace(rec) == "" {
			continue
		}
		f := strings.SplitN(rec, "\x1f", 4)
		if len(f) < 4 {
			continue
		}
		ch := obChange{Commit: f[0]}
		if sec, err := strconv.ParseInt(f[1], 10, 64); err == nil {
			ch.Time = time.Unix(sec, 0)
		}
		for _, l := range strings.Split(f[2], "\n") {
			k, v, ok := strings.Cut(l, ": ")
			if !ok {
				continue
			}
			v = strings.TrimSpace(v)
			switch k {
			case "Jazz-Session":
				ch.Session = v
			case "Jazz-Step":
				ch.Step, _ = strconv.Atoi(v)
			case "Jazz-Model":
				ch.Model = v
			case "Jazz-Prompt-Sha256":
				ch.Prompt = v
			case "Jazz-Reason":
				ch.Reason = v
			}
		}
		for _, l := range strings.Split(strings.TrimSpace(f[3]), "\n") {
			cols := strings.Fields(l)
			if len(cols) < 3 {
				continue
			}
			if cols[0] == "-" {
				ch.Binary = true
				continue
			}
			a, _ := strconv.Atoi(cols[0])
			d, _ := strconv.Atoi(cols[1])
			ch.Added += a
			ch.Deleted += d
		}
		res = append(res, ch)
	}
	return res, nil
}

// cmdChanges: jazz changes|blame <arquivo> [-n N]
func cmdChanges(root string, args []string, w io.Writer) int {
	file, limit := "", 50
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-n":
			if i+1 < len(args) {
				fmt.Sscan(args[i+1], &limit)
				i++
			}
		default:
			file = args[i]
		}
	}
	if file == "" {
		fmt.Fprintln(os.Stderr, "uso: jazz changes <arquivo> [-n N]   (quem mudou o arquivo: sessão, passo, modelo)")
		return 2
	}
	abs := file
	if !filepath.IsAbs(abs) {
		cwd, _ := os.Getwd()
		abs = filepath.Join(cwd, file)
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		fmt.Fprintln(os.Stderr, "jazz: o arquivo está fora da pasta do projeto", root)
		return 2
	}
	rel = filepath.ToSlash(rel)
	c := newCPStore(root)
	list, err := c.TraceLog(rel, limit)
	if err != nil {
		fmt.Fprintln(os.Stderr, "jazz:", err)
		return 1
	}
	if len(list) == 0 {
		fmt.Fprintln(w, "nenhuma mudança rastreada em", rel)
		return 0
	}
	fmt.Fprintf(w, "mudanças em %s (mais nova primeiro):\n", rel)
	for _, ch := range list {
		who := fmt.Sprintf("sessão %s · passo %d · %s · prompt %s", ch.Session, ch.Step, ch.Model, obShort(ch.Prompt))
		if ch.Reason == "turn-start" {
			who = "fora do jazz (antes de um turno da sessão " + ch.Session + ")"
		}
		diff := fmt.Sprintf("+%d −%d", ch.Added, ch.Deleted)
		if ch.Binary {
			diff = "binário"
		}
		fmt.Fprintf(w, "  %s  %s  %-9s %s  [%s]\n", ch.Time.Format("2006-01-02 15:04:05"), obShort(ch.Commit), diff, who, ch.Reason)
	}
	return 0
}
