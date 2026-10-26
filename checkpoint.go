package main

// Shadow-git checkpoints (gap #1 of docs/research/bob-kiro-devin.md, after the
// Q CLI's checkpoint.rs). The in-memory snapshots in tools.go only see write
// and edit; weak models often change files through bash (sed -i, > file), so
// /undo and /rewind used to miss those changes.
//
// A bare repository at dataDir()/checkpoints/<projectSlug>.git is used with
// --work-tree=<project root>. A snapshot is `update-index` + `write-tree`: a
// tree object, never a commit, so the user's identity is not needed and
// nothing touches the project's own .git. Snapshots are taken at the start of
// each turn and after every bash call that may write. The paths a bash call
// changed (tree diff before/after) are recorded on the current turn, and
// /undo and /rewind put exactly those paths back to the turn-start tree
// (checkout from the tree, or delete when the path did not exist then).
// Restoring per path, instead of `checkout -- .`, keeps edits the user made
// by hand between turns.
//
// .gitignore (and the project's .git/info/exclude) are respected, files over
// 1 MB are skipped, and checkpoints turn themselves off when the tree has more
// than 20k files, git is missing or older than 2.25, or any git call fails.
// The in-memory snapshots of write/edit keep working in every case.

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	cpMaxFiles          = 20000
	cpMaxFileSize int64 = 1 << 20
	cpGitBin            = "git"
	cpTimeout           = 60 * time.Second
	cpIndexMaxAge       = 48 * time.Hour
)

var errCPTooMany = errors.New("too many files")

type cpStore struct {
	mu     sync.Mutex
	root   string
	gitDir string
	index  string // per-process index, so two jazz processes never fight over index.lock
	git    string
	ready  bool
	off    string // why checkpoints are disabled ("" = usable)
}

func newCPStore(root string) *cpStore {
	if r, err := filepath.Abs(root); err == nil {
		root = r
	}
	return &cpStore{root: root}
}

// usable reports whether snapshots can be taken (initialising on first use).
func (c *cpStore) usable() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.initLocked() == nil
}

// disableLocked turns checkpoints off for the rest of the process.
func (c *cpStore) disableLocked(reason string) {
	if c.off == "" {
		c.off = reason
		dbg("checkpoint", map[string]any{"off": reason, "root": c.root})
	}
}

func (c *cpStore) initLocked() error {
	if c.off != "" {
		return errors.New(c.off)
	}
	if c.ready {
		return nil
	}
	git, err := exec.LookPath(cpGitBin)
	if err != nil {
		c.disableLocked("git ausente")
		return errors.New(c.off)
	}
	c.git = git
	out, err := c.cmd(nil, nil, "version")
	if err != nil || !cpGitAtLeast(string(out), 2, 25) {
		c.disableLocked("git antigo demais (precisa de 2.25+): " + strings.TrimSpace(string(out)))
		return errors.New(c.off)
	}
	c.gitDir = filepath.Join(dataDir(), "checkpoints", projectSlug(c.root)+".git")
	if _, err := os.Stat(filepath.Join(c.gitDir, "HEAD")); err != nil {
		if err := os.MkdirAll(filepath.Dir(c.gitDir), 0o700); err != nil {
			c.disableLocked("init: " + err.Error())
			return errors.New(c.off)
		}
		if out, err := c.cmd(nil, nil, "init", "-q", "--bare", c.gitDir); err != nil {
			c.disableLocked("init: " + strings.TrimSpace(string(out)) + " " + err.Error())
			return errors.New(c.off)
		}
	}
	c.writeExclude()
	c.index = filepath.Join(c.gitDir, fmt.Sprintf("index-%d", os.Getpid()))
	c.cleanIndexes()
	// seed from the shared warm index so the first snapshot uses its stat cache
	if b, err := os.ReadFile(filepath.Join(c.gitDir, "index")); err == nil {
		_ = os.WriteFile(c.index, b, 0o600)
	}
	c.ready = true
	return nil
}

// writeExclude copies the project's own .git/info/exclude and hides jazz's
// data dir when it lives inside the project.
func (c *cpStore) writeExclude() {
	var b strings.Builder
	b.WriteString("# written by jazz checkpoints\n")
	if x, err := os.ReadFile(filepath.Join(c.root, ".git", "info", "exclude")); err == nil {
		b.Write(x)
		b.WriteString("\n")
	}
	if rel, err := filepath.Rel(c.root, dataDir()); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
		b.WriteString("/" + filepath.ToSlash(rel) + "/\n")
	}
	_ = os.MkdirAll(filepath.Join(c.gitDir, "info"), 0o700)
	_ = os.WriteFile(filepath.Join(c.gitDir, "info", "exclude"), []byte(b.String()), 0o600)
}

// cleanIndexes removes per-process indexes left behind by old processes.
func (c *cpStore) cleanIndexes() {
	ents, _ := os.ReadDir(c.gitDir)
	for _, e := range ents {
		if !strings.HasPrefix(e.Name(), "index-") {
			continue
		}
		if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > cpIndexMaxAge {
			os.Remove(filepath.Join(c.gitDir, e.Name()))
		}
	}
}

var cpVersionRe = lazyRx(`(\d+)\.(\d+)`)

func cpGitAtLeast(version string, major, minor int) bool {
	m := cpVersionRe.FindStringSubmatch(version)
	if m == nil {
		return false
	}
	a, _ := strconv.Atoi(m[1])
	b, _ := strconv.Atoi(m[2])
	return a > major || a == major && b >= minor
}

// env is the process environment without the caller's GIT_* variables (a
// jazz started from a git hook must not snapshot into the project's repo).
func (c *cpStore) env() []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			env = append(env, kv)
		}
	}
	env = append(env, "GIT_TERMINAL_PROMPT=0", "GIT_LITERAL_PATHSPECS=1", "GIT_OPTIONAL_LOCKS=0")
	if c.index != "" { // set once the shadow repo exists
		env = append(env, "GIT_DIR="+c.gitDir, "GIT_WORK_TREE="+c.root, "GIT_INDEX_FILE="+c.index)
	}
	return env
}

func (c *cpStore) gitCmd(ctx context.Context, args ...string) *exec.Cmd {
	full := append([]string{
		"-c", "core.bare=false", "-c", "core.autocrlf=false", "-c", "core.safecrlf=false",
		"-c", "core.fsmonitor=false", "-c", "core.quotepath=false", "-c", "gc.auto=0",
		"-c", "core.hooksPath=" + os.DevNull,
	}, args...)
	cmd := exec.CommandContext(ctx, c.git, full...)
	cmd.Dir = c.root
	cmd.Env = c.env()
	return cmd
}

// cmd runs git with stdin and returns stdout (stderr goes into the error).
func (c *cpStore) cmd(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), cpTimeout)
		defer cancel()
	}
	cmd := c.gitCmd(ctx, args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("git %s: %v: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// listZ streams NUL-separated paths from git, stopping (errCPTooMany) once
// more than limit paths came out, so a huge tree is never walked to the end.
func (c *cpStore) listZ(limit int, args ...string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), cpTimeout)
	defer cancel()
	cmd := c.gitCmd(ctx, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	sc := bufio.NewScanner(pipe)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	sc.Split(cpSplitNUL)
	var out []string
	for sc.Scan() {
		out = append(out, sc.Text())
		if len(out) > limit {
			cancel()
			io.Copy(io.Discard, pipe)
			cmd.Wait()
			return nil, errCPTooMany
		}
	}
	if err := cmd.Wait(); err != nil {
		return nil, fmt.Errorf("git %s: %v: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func cpSplitNUL(data []byte, atEOF bool) (int, []byte, error) {
	if i := bytes.IndexByte(data, 0); i >= 0 {
		return i + 1, data[:i], nil
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// Snapshot records the current tree and returns its id ("" when disabled).
func (c *cpStore) Snapshot() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.initLocked(); err != nil {
		return "", err
	}
	tree, err := c.snapshotLocked()
	if err != nil {
		if errors.Is(err, errCPTooMany) {
			c.disableLocked(fmt.Sprintf("mais de %d arquivos no projeto", cpMaxFiles))
		} else {
			c.disableLocked("snapshot: " + err.Error())
		}
		return "", err
	}
	return tree, nil
}

func (c *cpStore) snapshotLocked() (string, error) {
	cached, err := c.listZ(cpMaxFiles, "ls-files", "-z", "--cached")
	if err != nil {
		return "", err
	}
	others, err := c.listZ(cpMaxFiles-len(cached), "ls-files", "-z", "--others", "--exclude-standard")
	if err != nil {
		return "", err
	}
	inIndex := make(map[string]bool, len(cached))
	for _, p := range cached {
		inIndex[p] = true
	}
	var add, drop bytes.Buffer
	for _, list := range [][]string{cached, others} {
		for _, p := range list {
			fi, err := os.Lstat(filepath.Join(c.root, filepath.FromSlash(p)))
			switch {
			case err != nil: // deleted: --remove drops it from the index
				if inIndex[p] {
					add.WriteString(p + "\x00")
				}
			case fi.Mode().IsRegular() && fi.Size() <= cpMaxFileSize, fi.Mode()&os.ModeSymlink != 0:
				add.WriteString(p + "\x00")
			default: // too big, a directory (nested repo), fifo, device...
				if inIndex[p] {
					drop.WriteString(p + "\x00")
				}
			}
		}
	}
	if add.Len() > 0 {
		if _, err := c.cmd(nil, add.Bytes(), "update-index", "--add", "--remove", "-z", "--stdin"); err != nil {
			return "", err
		}
	}
	if drop.Len() > 0 {
		if _, err := c.cmd(nil, drop.Bytes(), "update-index", "--force-remove", "-z", "--stdin"); err != nil {
			return "", err
		}
	}
	out, err := c.cmd(nil, nil, "write-tree")
	if err != nil {
		return "", err
	}
	c.shareIndex()
	return strings.TrimSpace(string(out)), nil
}

// shareIndex publishes this process's index as the warm seed for the next one.
func (c *cpStore) shareIndex() {
	b, err := os.ReadFile(c.index)
	if err != nil {
		return
	}
	tmp := c.index + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		if os.Rename(tmp, filepath.Join(c.gitDir, "index")) != nil {
			os.Remove(tmp)
		}
	}
}

// Changed lists the paths (relative, slash-separated) that differ between two trees.
func (c *cpStore) Changed(a, b string) ([]string, error) {
	if a == "" || b == "" || a == b {
		return nil, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.initLocked(); err != nil {
		return nil, err
	}
	out, err := c.cmd(nil, nil, "diff-tree", "-r", "-z", "--name-only", "--no-renames", a, b)
	if err != nil {
		return nil, err
	}
	return cpSplitZ(out), nil
}

func cpSplitZ(b []byte) []string {
	var out []string
	for _, p := range bytes.Split(b, []byte{0}) {
		if len(p) > 0 {
			out = append(out, string(p))
		}
	}
	return out
}

// Restore puts rels back to how they were in tree: checked out from it, or
// deleted when tree did not have them.
func (c *cpStore) Restore(tree string, rels []string) error {
	if tree == "" || len(rels) == 0 {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.ready {
		return errors.New("checkpoints indisponíveis")
	}
	out, err := c.cmd(nil, nil, "ls-tree", "-r", "-z", "--name-only", tree)
	if err != nil {
		return err
	}
	had := map[string]bool{}
	for _, p := range cpSplitZ(out) {
		had[p] = true
	}
	var co bytes.Buffer
	var firstErr error
	for _, r := range rels {
		abs := filepath.Join(c.root, filepath.FromSlash(r))
		// a directory that bash turned into a symlink out of the project must
		// not be written or deleted through (F8)
		if !cpParentInside(c.root, abs) {
			if firstErr == nil {
				firstErr = fmt.Errorf("não restaurei %s: a pasta virou um link para fora do projeto", r)
			}
			continue
		}
		if had[r] {
			if fi, err := os.Lstat(abs); err == nil && fi.Mode()&os.ModeSymlink != 0 {
				os.Remove(abs) // the file became a symlink: restore the file, not its target
			}
			co.WriteString(r + "\x00")
			continue
		}
		if err := os.Remove(abs); err != nil && !os.IsNotExist(err) && firstErr == nil {
			firstErr = err
		}
		cpPruneDirs(c.root, filepath.Dir(abs))
	}
	if co.Len() > 0 {
		if _, err := c.cmd(nil, co.Bytes(), "checkout", tree, "--pathspec-from-file=-", "--pathspec-file-nul"); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// cpParentInside reports whether the nearest existing parent of abs resolves
// to a directory inside root.
func cpParentInside(root, abs string) bool {
	for d := filepath.Dir(abs); ; d = filepath.Dir(d) {
		if _, err := os.Lstat(d); err == nil {
			return pfInsideRoot(d, root)
		}
		if d == filepath.Dir(d) || !strings.HasPrefix(d, root) {
			return false
		}
	}
}

// cpPruneDirs removes now-empty directories from dir up to (not including) root.
func cpPruneDirs(root, dir string) {
	for dir != root && strings.HasPrefix(dir, root+string(filepath.Separator)) {
		if os.Remove(dir) != nil { // fails when not empty
			return
		}
		dir = filepath.Dir(dir)
	}
}

// ---------- Toolbox hooks ----------

// checkpoints returns the shadow-git store, or nil when checkpoints are off.
func (t *Toolbox) checkpoints() *cpStore {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.Checkpoints {
		return nil
	}
	if t.cp == nil {
		t.cp = newCPStore(t.Root)
	}
	return t.cp
}

// cpSnapshot takes a snapshot, returning "" when checkpoints are off or fail.
func (t *Toolbox) cpSnapshot() string {
	c := t.checkpoints()
	if c == nil {
		return ""
	}
	tree, _ := c.Snapshot()
	return tree
}

// cpAfterBash snapshots after a bash call that may have written and records
// the paths it changed on the current turn.
func (t *Toolbox) cpAfterBash(cmd string) {
	if IsReadOnly(cmd) {
		return
	}
	c := t.checkpoints()
	if c == nil {
		return
	}
	t.mu.Lock()
	n := len(t.turns)
	prev := ""
	if n > 0 {
		prev = t.turns[n-1].cpLast
	}
	t.mu.Unlock()
	if prev == "" { // no turn-start snapshot to diff against
		return
	}
	tree, err := c.Snapshot()
	if err != nil || tree == "" {
		return
	}
	detail, err := c.ChangedDetail(prev, tree)
	if err != nil {
		return
	}
	t.cpAuditChanges(detail) // audit: path + blob before/after (checkpoint_trace.go)
	changed := make([]string, len(detail))
	for i, ch := range detail {
		changed[i] = ch.Path
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.turns) != n || t.turns[n-1].cpLast != prev {
		return // the turn moved on meanwhile
	}
	cur := &t.turns[n-1]
	cur.cpLast = tree
	for _, r := range changed {
		if cur.bash == nil {
			cur.bash = map[string]bool{}
		}
		cur.bash[filepath.Join(t.Root, filepath.FromSlash(r))] = true
	}
}

// cpRestore puts back the files bash changed during ts to the turn-start
// tree. Files written by write/edit before any bash change are left to the
// in-memory snapshot, which also covers ignored and big files. Callers hold t.mu.
func (t *Toolbox) cpRestore(ts turnSnap) ([]string, error) {
	if len(ts.bash) == 0 || ts.cpStart == "" || t.cp == nil {
		return nil, nil
	}
	var rels, paths []string
	for p := range ts.bash {
		if s, ok := ts.files[p]; ok && !s.afterBash {
			continue
		}
		r, err := filepath.Rel(t.Root, p)
		if err != nil || strings.HasPrefix(r, "..") {
			continue
		}
		rels = append(rels, filepath.ToSlash(r))
		paths = append(paths, t.rel(p))
		delete(t.readState, p)
	}
	sort.Strings(rels)
	return paths, t.cp.Restore(ts.cpStart, rels)
}

// BashTracked reports whether /undo and /rewind also cover changes made through bash.
func (t *Toolbox) BashTracked() bool {
	c := t.checkpoints()
	return c != nil && c.usable()
}
