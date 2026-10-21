package main

// worktree.go: session isolation in a git worktree.
//
// `jazz --worktree [name]` (or --worktree=name) creates
// <repo>/.jazz/worktrees/<name> on a new branch jazz/<name> from HEAD and runs
// the session (interactive or -p) with that directory as the project root, so
// the fence, the sandbox and every tool follow it. The main checkout is never
// touched. `jazz worktree merge <name>` brings the work back with
// `git merge --no-ff` into the branch that was checked out when the worktree
// was made; `jazz worktree list` / `rm` manage them. Everything shells out to
// git; nothing goes into the system prompt besides the root path itself.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// wtDir is where worktrees live, relative to the repository top level.
const wtDir = ".jazz/worktrees"

// wtNameRE: a worktree name is also a branch suffix and a directory name.
var wtNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// wtMeta is <repo>/.jazz/worktrees/<name>.json.
type wtMeta struct {
	Name    string    `json:"name"`
	Branch  string    `json:"branch"` // jazz/<name>
	Orig    string    `json:"orig"`   // branch (or commit, when detached) checked out at creation
	Base    string    `json:"base"`   // commit the worktree started from
	Path    string    `json:"path"`
	Created time.Time `json:"created"`
}

// wtActive is the worktree this process runs in (nil = none).
var wtActive *wtMeta

// wtActiveRepo is the main repository top level of wtActive.
var wtActiveRepo string

// wtParseArgs removes --worktree / --worktree=name / --worktree name from
// args. A following word is taken as the name only when it looks like one
// (no spaces, not a flag, not a subcommand): `jazz --worktree "fix the bug"`
// keeps the prompt; --worktree=name is never ambiguous.
func wtParseArgs(args []string) (out []string, on bool, name string, err error) {
	for i := 0; i < len(args); i++ {
		s := args[i]
		if s == "--" {
			out = append(out, args[i:]...)
			break
		}
		switch {
		case s == "--worktree" || s == "-worktree":
			on = true
			if i+1 < len(args) {
				nx := args[i+1]
				if wtNameRE.MatchString(nx) && !strings.HasPrefix(nx, "-") && !pfSubcommands[nx] {
					name = nx
					i++
				}
			}
			continue
		case strings.HasPrefix(s, "--worktree=") || strings.HasPrefix(s, "-worktree="):
			on = true
			name = s[strings.Index(s, "=")+1:]
			if name != "" && !wtNameRE.MatchString(name) {
				return nil, false, "", fmt.Errorf("--worktree: nome inválido %q (letras, números, . _ -)", name)
			}
			continue
		}
		out = append(out, s)
	}
	return out, on, name, nil
}

// wtGit runs git in dir and returns trimmed stdout (stderr in the error).
func wtGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(se.String())
		if msg == "" {
			msg = err.Error()
		}
		return strings.TrimSpace(so.String()), fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return strings.TrimSpace(so.String()), nil
}

// wtIdentity adds a fallback author when git has none configured (fresh
// machines, CI), so the commit/merge does not fail on "please tell me who you are".
func wtIdentity(dir string) []string {
	if e, _ := wtGit(dir, "config", "user.email"); e != "" {
		return nil
	}
	return []string{"-c", "user.name=jazz", "-c", "user.email=jazz@localhost"}
}

// wtRepo returns the main repository top level for dir (also from inside a
// linked worktree: the common dir's parent).
func wtRepo(dir string) (string, error) {
	common, err := wtGit(dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		top, err2 := wtGit(dir, "rev-parse", "--show-toplevel")
		if err2 != nil {
			return "", errors.New("não é um repositório git (o --worktree precisa de git)")
		}
		return filepath.FromSlash(top), nil // git prints C:/x on Windows
	}
	common = filepath.FromSlash(common)
	if filepath.Base(common) == ".git" {
		return filepath.Dir(common), nil
	}
	top, err := wtGit(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", errors.New("não é um repositório git (o --worktree precisa de git)")
	}
	return filepath.FromSlash(top), nil
}

func wtMetaPath(repo, name string) string { return filepath.Join(repo, wtDir, name+".json") }

func wtLoad(repo, name string) (*wtMeta, error) {
	if !wtNameRE.MatchString(name) {
		return nil, fmt.Errorf("nome de worktree inválido: %q", name)
	}
	b, err := os.ReadFile(wtMetaPath(repo, name))
	if err != nil {
		return nil, fmt.Errorf("worktree %q não existe (jazz worktree list)", name)
	}
	var m wtMeta
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("worktree %q: metadados corrompidos: %v", name, err)
	}
	return &m, nil
}

// wtExclude adds the worktree dir to .git/info/exclude (never .gitignore:
// the user's tracked files stay untouched).
func wtExclude(repo string) error {
	gd, err := wtGit(repo, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return err
	}
	p := filepath.Join(filepath.FromSlash(gd), "info", "exclude")
	b, _ := os.ReadFile(p)
	line := "/" + wtDir + "/"
	for _, l := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(l) == line {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	s := string(b)
	if s != "" && !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	s += "# jazz --worktree\n" + line + "\n"
	return os.WriteFile(p, []byte(s), 0o644)
}

// wtCreate makes the worktree for root (any directory inside the repo).
func wtCreate(root, name string) (*wtMeta, string, error) {
	repo, err := wtRepo(root)
	if err != nil {
		return nil, "", err
	}
	if name == "" {
		name = "wt-" + time.Now().Format("20060102-150405")
	}
	if !wtNameRE.MatchString(name) {
		return nil, "", fmt.Errorf("nome de worktree inválido: %q", name)
	}
	path := filepath.Join(repo, wtDir, name)
	if _, err := os.Stat(path); err == nil {
		return nil, "", fmt.Errorf("o worktree %q já existe em %s (jazz worktree rm %s, ou outro nome)", name, path, name)
	}
	base, err := wtGit(repo, "rev-parse", "HEAD")
	if err != nil {
		return nil, "", errors.New("o repositório não tem nenhum commit ainda: faça o primeiro commit antes do --worktree")
	}
	orig, err := wtGit(repo, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil || orig == "" {
		orig = base // detached HEAD: merge goes back onto the commit
	}
	branch := "jazz/" + name
	if _, err := wtGit(repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
		return nil, "", fmt.Errorf("o branch %s já existe", branch)
	}
	if err := wtExclude(repo); err != nil {
		return nil, "", err
	}
	if err := os.MkdirAll(filepath.Join(repo, wtDir), 0o755); err != nil {
		return nil, "", err
	}
	if _, err := wtGit(repo, "worktree", "add", "-b", branch, path, base); err != nil {
		return nil, "", err
	}
	m := &wtMeta{Name: name, Branch: branch, Orig: orig, Base: base, Path: path, Created: time.Now().UTC()}
	b, _ := json.MarshalIndent(m, "", "  ")
	if err := writeFileAtomic(wtMetaPath(repo, name), append(b, '\n'), 0o644); err != nil {
		return nil, "", err
	}
	wtCarryTrust(repo, path)
	return m, repo, nil
}

// wtCarryTrust: a worktree of a trusted folder is trusted too, as long as its
// instruction files are byte-identical to the ones the user trusted.
func wtCarryTrust(repo, path string) {
	if !pfFolderTrusted(repo) {
		return
	}
	if !bytes.Equal(pfInstructionsBlob(repo), pfInstructionsBlob(path)) {
		return // HEAD differs from the trusted working copy: the gate asks
	}
	_ = Trust(path, "folder", pfFolderMark)
	_ = Trust(path, "instructions", pfInstructionsBlob(path))
}

// wtStart is the --worktree hook in main: it creates the worktree and returns
// the new project root (the same subdirectory inside the worktree).
func wtStart(root, name string, w io.Writer) (string, error) {
	m, repo, err := wtCreate(root, name)
	if err != nil {
		return "", err
	}
	wtActive, wtActiveRepo = m, repo
	newRoot := m.Path
	if rel, err := filepath.Rel(pfTrustRoot(repo), pfTrustRoot(root)); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
		if st, err := os.Stat(filepath.Join(m.Path, rel)); err == nil && st.IsDir() {
			newRoot = filepath.Join(m.Path, rel)
		}
	}
	fmt.Fprintf(w, "jazz: worktree %s criado em %s (branch %s, a partir de %s)\n", m.Name, m.Path, m.Branch, wtShort(m.Orig))
	return newRoot, nil
}

func wtShort(ref string) string {
	if len(ref) == 40 && !strings.ContainsAny(ref, "/-_") {
		return ref[:10]
	}
	return ref
}

// wtExitHint tells how to bring the work back (printed when the session ends).
func wtExitHint(w io.Writer) {
	if wtActive == nil {
		return
	}
	m := wtActive
	dirty, _ := wtGit(m.Path, "status", "--porcelain")
	ahead, _ := wtGit(wtActiveRepo, "rev-list", "--count", m.Orig+".."+m.Branch)
	state := "sem mudanças"
	switch {
	case dirty != "" && ahead != "0" && ahead != "":
		state = ahead + " commit(s) + mudanças não commitadas"
	case dirty != "":
		state = "mudanças não commitadas"
	case ahead != "0" && ahead != "":
		state = ahead + " commit(s)"
	}
	fmt.Fprintf(w, "worktree %s (%s): %s\n", m.Name, m.Branch, state)
	fmt.Fprintf(w, "  mesclar em %s:  jazz worktree merge %s\n", wtShort(m.Orig), m.Name)
	fmt.Fprintf(w, "  descartar:       jazz worktree rm %s --force\n", m.Name)
}

// wtStatus summarizes one worktree for list and /worktree.
func wtStatus(repo string, m *wtMeta) string {
	if _, err := os.Stat(m.Path); err != nil {
		return "pasta removida"
	}
	var parts []string
	if d, _ := wtGit(m.Path, "status", "--porcelain"); d != "" {
		parts = append(parts, fmt.Sprintf("sujo (%d arquivo(s))", len(strings.Split(d, "\n"))))
	} else {
		parts = append(parts, "limpo")
	}
	if a, err := wtGit(repo, "rev-list", "--count", m.Orig+".."+m.Branch); err == nil {
		parts = append(parts, a+" à frente de "+wtShort(m.Orig))
	}
	return strings.Join(parts, ", ")
}

// wtNames lists the worktrees recorded in repo.
func wtNames(repo string) []string {
	ents, _ := os.ReadDir(filepath.Join(repo, wtDir))
	var out []string
	for _, e := range ents {
		if n, ok := strings.CutSuffix(e.Name(), ".json"); ok && !e.IsDir() && wtNameRE.MatchString(n) {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

func wtList(repo string, w io.Writer) {
	names := wtNames(repo)
	if len(names) == 0 {
		fmt.Fprintln(w, "nenhum worktree do jazz (crie com: jazz --worktree [nome])")
		return
	}
	for _, n := range names {
		m, err := wtLoad(repo, n)
		if err != nil {
			fmt.Fprintf(w, "%-24s %v\n", n, err)
			continue
		}
		fmt.Fprintf(w, "%-24s %-28s %s\n", n, m.Branch, wtStatus(repo, m))
	}
}

// wtAsk reads a yes/no answer (default no).
func wtAsk(in io.Reader, w io.Writer, q string) bool {
	fmt.Fprint(w, q+" [s/N] ")
	line, _ := bufio.NewReader(in).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "s", "sim", "y", "yes":
		return true
	}
	return false
}

// wtMerge brings jazz/<name> into the original branch with --no-ff.
func wtMerge(repo, name string, yes bool, in io.Reader, w io.Writer) error {
	m, err := wtLoad(repo, name)
	if err != nil {
		return err
	}
	cur, _ := wtGit(repo, "symbolic-ref", "--quiet", "--short", "HEAD")
	if cur == "" {
		cur, _ = wtGit(repo, "rev-parse", "HEAD")
	}
	if cur != m.Orig {
		return fmt.Errorf("o repositório principal está em %s; o worktree saiu de %s: faça git checkout %s antes", wtShort(cur), wtShort(m.Orig), wtShort(m.Orig))
	}
	if d, _ := wtGit(repo, "status", "--porcelain", "--untracked-files=no"); d != "" {
		return errors.New("o repositório principal tem mudanças não commitadas: commit ou stash antes de mesclar")
	}
	if _, err := os.Stat(m.Path); err == nil {
		if d, _ := wtGit(m.Path, "status", "--porcelain"); d != "" {
			fmt.Fprintf(w, "o worktree %s tem mudanças não commitadas:\n%s\n", name, d)
			if !yes && !wtAsk(in, w, "commitar tudo como \"jazz: trabalho no worktree "+name+"\"?") {
				return errors.New("cancelado: commit no worktree antes de mesclar")
			}
			if _, err := wtGit(m.Path, "add", "-A"); err != nil {
				return err
			}
			if _, err := wtGit(m.Path, append(wtIdentity(m.Path), "commit", "-q", "-m", "jazz: trabalho no worktree "+name)...); err != nil {
				return err
			}
		}
	}
	stat, err := wtGit(repo, "diff", "--stat", m.Orig+"..."+m.Branch)
	if err != nil {
		return err
	}
	if stat == "" {
		fmt.Fprintf(w, "nada pra mesclar: %s não tem commits além de %s\n", m.Branch, wtShort(m.Orig))
		return nil
	}
	fmt.Fprintf(w, "%s → %s:\n%s\n", m.Branch, wtShort(m.Orig), stat)
	if !yes && !wtAsk(in, w, "mesclar com git merge --no-ff?") {
		return errors.New("cancelado")
	}
	out, err := wtGit(repo, append(wtIdentity(repo), "merge", "--no-ff", "-m", "jazz: mescla o worktree "+name, m.Branch)...)
	if err != nil {
		return fmt.Errorf("%v\n(resolva os conflitos no repositório principal e commit, ou git merge --abort)", err)
	}
	if out != "" {
		fmt.Fprintln(w, out)
	}
	fmt.Fprintf(w, "mesclado em %s. Pra apagar o worktree: jazz worktree rm %s\n", wtShort(m.Orig), name)
	return nil
}

// wtRemove deletes the worktree and its branch; unmerged work needs force.
func wtRemove(repo, name string, force bool, w io.Writer) error {
	m, err := wtLoad(repo, name)
	if err != nil {
		return err
	}
	if !force {
		if _, err := os.Stat(m.Path); err == nil {
			if d, _ := wtGit(m.Path, "status", "--porcelain"); d != "" {
				return fmt.Errorf("o worktree %s tem mudanças não commitadas (use --force pra descartar)", name)
			}
		}
		if a, _ := wtGit(repo, "rev-list", "--count", m.Orig+".."+m.Branch); a != "" && a != "0" {
			if _, err := wtGit(repo, "merge-base", "--is-ancestor", m.Branch, "HEAD"); err != nil {
				return fmt.Errorf("%s tem %s commit(s) não mesclado(s) (jazz worktree merge %s, ou --force pra descartar)", m.Branch, a, name)
			}
		}
	}
	if _, err := os.Stat(m.Path); err == nil {
		args := []string{"worktree", "remove", m.Path}
		if force {
			args = []string{"worktree", "remove", "--force", m.Path}
		}
		if _, err := wtGit(repo, args...); err != nil {
			return err
		}
	} else {
		_, _ = wtGit(repo, "worktree", "prune")
	}
	if _, err := wtGit(repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+m.Branch); err == nil {
		if _, err := wtGit(repo, "branch", "-D", m.Branch); err != nil {
			return err
		}
	}
	_ = os.Remove(wtMetaPath(repo, name))
	fmt.Fprintf(w, "worktree %s removido (branch %s apagado)\n", name, m.Branch)
	return nil
}

// cmdWorktree implements `jazz worktree list|merge|rm`. Exit 0 ok, 1 error, 2 usage.
func cmdWorktree(root string, args []string, in io.Reader, w io.Writer) int {
	usageW := "uso: jazz worktree list | merge <nome> [--yes] | rm <nome> [--force]   (criar: jazz --worktree [nome])"
	repo, err := wtRepo(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "jazz:", err)
		return 1
	}
	if len(args) == 0 {
		wtList(repo, w)
		return 0
	}
	var pos []string
	yes, force := false, false
	for _, a := range args[1:] {
		switch a {
		case "--yes", "-y", "-yes":
			yes = true
		case "--force", "-f", "-force":
			force = true
		default:
			pos = append(pos, a)
		}
	}
	switch args[0] {
	case "list", "ls":
		wtList(repo, w)
		return 0
	case "merge", "rm", "remove":
		if len(pos) != 1 {
			fmt.Fprintln(os.Stderr, usageW)
			return 2
		}
		if args[0] == "merge" {
			err = wtMerge(repo, pos[0], yes, in, w)
		} else {
			err = wtRemove(repo, pos[0], force, w)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "jazz:", err)
			return 1
		}
		return 0
	}
	fmt.Fprintln(os.Stderr, usageW)
	return 2
}

// wtCommand handles /worktree in the interactive session; false = not ours.
func (a *App) wtCommand(cmd, arg string) bool {
	if cmd != "/worktree" {
		return false
	}
	var b bytes.Buffer
	switch {
	case strings.TrimSpace(arg) == "" && wtActive != nil:
		fmt.Fprintf(&b, "  worktree %s · branch %s · %s\n  pasta %s\n", wtActive.Name, wtActive.Branch, wtStatus(wtActiveRepo, wtActive), wtActive.Path)
		fmt.Fprintf(&b, "  ao sair: jazz worktree merge %s (mescla em %s)\n", wtActive.Name, wtShort(wtActive.Orig))
	default:
		repo, err := wtRepo(a.root)
		if err != nil {
			a.ui.Warn(err.Error())
			return true
		}
		wtList(repo, &b)
		if wtActive == nil {
			b.WriteString("pra isolar uma sessão: jazz --worktree [nome]\n")
		}
	}
	a.ui.print(sanitizeTerm(b.String()))
	return true
}
