package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// Skills from GitHub (or any git URL / local folder), like Claude Code and
// Codex: `jazz skill install <url>` clones, finds every SKILL.md (root, a
// /tree/<ref>/<path> subfolder, or a repo with many skills), shows what each
// skill carries (scripts included) and copies it into
// ~/.config/jazz-cli/skills/<name> (or .jazz/skills with --project), where
// findSkills lists it and the fence and the sandbox let the model read it.
// Demo on 12/11: "instala essa skill e faz um vídeo" had no way to install.

const (
	siMaxFiles = 3000
	siMaxBytes = 80 << 20
	siOrigin   = ".jazz-origin.json"
)

type siSource struct {
	URL, Ref, Sub string // clone URL, branch/tag ("" = default), subfolder
	Local         string // a local folder instead of a clone
}

type siOriginRec struct {
	Source    string `json:"source"`
	URL       string `json:"url,omitempty"`
	Ref       string `json:"ref,omitempty"`
	Sub       string `json:"sub,omitempty"`
	Commit    string `json:"commit,omitempty"`
	Installed string `json:"installed"`
}

type siFound struct {
	Name, Desc, Dir string
	Scripts         []string
	Files           int
	Bytes           int64
}

var (
	siTreeRE  = lazyRx(`^https?://(github\.com|gitlab\.com|codeberg\.org)/([\w.-]+)/([\w.-]+?)(?:\.git)?/(?:-/)?(?:tree|blob)/([^/]+)(?:/(.*))?$`)
	siRepoRE  = lazyRx(`^https?://(github\.com|gitlab\.com|codeberg\.org|bitbucket\.org)/([\w.-]+)/([\w.-]+?)(?:\.git)?/?$`)
	siShortRE = lazyRx(`^([\w.-]+)/([\w.-]+)$`)
	siNameRE  = lazyRx(`[^a-z0-9._-]+`)
)

// siParse understands github URLs (repo, /tree/<ref>/<path>, /blob/.../SKILL.md),
// owner/repo shorthand, git@ and other git URLs, and local folders.
func siParse(arg string) (siSource, error) {
	a := strings.TrimSpace(arg)
	if a == "" {
		return siSource{}, errors.New("falta a URL ou a pasta da skill")
	}
	if fi, err := os.Stat(a); err == nil && fi.IsDir() {
		abs, _ := filepath.Abs(a)
		return siSource{Local: abs}, nil
	}
	if m := siTreeRE.FindStringSubmatch(a); m != nil {
		sub := strings.TrimSuffix(m[5], "/SKILL.md")
		if sub == "SKILL.md" {
			sub = ""
		}
		return siSource{URL: "https://" + m[1] + "/" + m[2] + "/" + m[3] + ".git", Ref: m[4], Sub: sub}, nil
	}
	if m := siRepoRE.FindStringSubmatch(a); m != nil {
		return siSource{URL: "https://" + m[1] + "/" + m[2] + "/" + m[3] + ".git"}, nil
	}
	if strings.HasPrefix(a, "git@") || strings.HasSuffix(a, ".git") || strings.HasPrefix(a, "https://") || strings.HasPrefix(a, "ssh://") || strings.HasPrefix(a, "file://") {
		return siSource{URL: a}, nil
	}
	if m := siShortRE.FindStringSubmatch(a); m != nil {
		return siSource{URL: "https://github.com/" + m[1] + "/" + m[2] + ".git"}, nil
	}
	return siSource{}, fmt.Errorf("não entendi %q: use uma URL do GitHub (https://github.com/dono/repo[/tree/main/pasta]), dono/repo ou uma pasta local", a)
}

func siGit(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(scrubEnv(os.Environ()), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=", "SSH_ASKPASS=", "LC_ALL=C")
	b, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("git %s: %w", args[0], ctx.Err())
		}
		return "", fmt.Errorf("git %s: %v: %s", args[0], err, oneLine(string(b), 200))
	}
	return strings.TrimSpace(string(b)), nil
}

type siProgressKey struct{}

// siWithProgress: ctx whose installs report what they download through say
// ("baixando github.com/x/y (subpasta z)").
func siWithProgress(ctx context.Context, say func(string)) context.Context {
	return context.WithValue(ctx, siProgressKey{}, say)
}

func siSay(ctx context.Context, msg string) {
	if say, _ := ctx.Value(siProgressKey{}).(func(string)); say != nil {
		say(msg)
	}
}

// siShowURL: the clone URL as the user reads it (no scheme, no .git, no
// credentials).
func siShowURL(u string) string {
	u = strings.TrimSuffix(u, ".git")
	for _, p := range []string{"https://", "http://", "ssh://", "git://", "file://"} {
		u = strings.TrimPrefix(u, p)
	}
	if at := strings.Index(u, "@"); at >= 0 && !strings.Contains(u[:at], "/") {
		u = u[at+1:]
	}
	return u
}

// siGitErr turns git's clone errors into something the user can act on: a
// typo'd or private repo makes git ask for a username (GIT_TERMINAL_PROMPT=0
// then fails with "could not read Username"), which says nothing to them.
func siGitErr(err error, src siSource) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return errors.New("download da skill cancelado")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return errors.New("download da skill demorou demais (mais de 3 min) e foi cancelado")
	}
	m := strings.ToLower(err.Error())
	show := siShowURL(src.URL)
	switch {
	case src.Ref != "" && (strings.Contains(m, "remote branch") && strings.Contains(m, "not found") || strings.Contains(m, "couldn't find remote ref")):
		return fmt.Errorf("o branch/tag %q não existe em %s", src.Ref, show)
	case strings.Contains(m, "could not read username"), strings.Contains(m, "could not read password"),
		strings.Contains(m, "repository not found"), strings.Contains(m, "authentication failed"),
		strings.Contains(m, "terminal prompts disabled"), strings.Contains(m, "does not appear to be a git repository"),
		strings.Contains(m, "could not read from remote repository"), strings.Contains(m, "permission denied"),
		strings.Contains(m, "error: 404"), strings.Contains(m, "not found"):
		return fmt.Errorf("repositório não encontrado ou privado: %s (confira a URL; repositório privado precisa de acesso configurado no git)", show)
	case strings.Contains(m, "could not resolve host"), strings.Contains(m, "unable to access"), strings.Contains(m, "connection timed out"), strings.Contains(m, "network is unreachable"):
		return fmt.Errorf("não consegui acessar %s (sem rede?): %s", show, oneLine(err.Error(), 160))
	}
	return err
}

// siTmpRoot: where clones go. Not os.TempDir(): on small boxes /tmp is a
// RAM tmpfs (1 GB on the D610) and a repo full of videos fills it. jazz's data
// dir is on disk; os.TempDir() only when it can't be created.
func siTmpRoot() string {
	d := filepath.Join(dataDir(), "tmp")
	if err := os.MkdirAll(d, 0o700); err != nil {
		return ""
	}
	// a clone left by a killed jazz (SIGKILL, power loss) is swept here
	if ents, err := os.ReadDir(d); err == nil {
		for _, e := range ents {
			if !strings.HasPrefix(e.Name(), "jazz-skill-") {
				continue
			}
			if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > 6*time.Hour {
				os.RemoveAll(filepath.Join(d, e.Name()))
			}
		}
	}
	return d
}

// siSubOK: a subfolder from a URL is a plain relative path (it becomes a
// sparse-checkout pattern and a path under the clone).
func siSubOK(sub string) bool {
	if sub == "" || strings.HasPrefix(sub, "/") || strings.ContainsAny(sub, "\\*?[!#\n\r") {
		return false
	}
	for _, p := range strings.Split(sub, "/") {
		if p == "" || p == "." || p == ".." {
			return false
		}
	}
	return true
}

// siFetch makes the source available locally; cleanup removes the clone.
// With a subfolder (/tree/<ref>/<sub>) only that folder is downloaded: a
// blobless partial clone plus a sparse checkout; a server or git that can't
// do that gets the plain shallow clone.
func siFetch(ctx context.Context, src siSource) (base, commit string, cleanup func(), err error) {
	if src.Local != "" {
		return src.Local, "", func() {}, nil
	}
	if _, err := exec.LookPath("git"); err != nil {
		return "", "", nil, errors.New("precisa do git instalado para baixar a skill")
	}
	sub := strings.Trim(src.Sub, "/")
	if sub != "" && !siSubOK(sub) {
		return "", "", nil, fmt.Errorf("subpasta inválida: %q", src.Sub)
	}
	tmp, err := os.MkdirTemp(siTmpRoot(), "jazz-skill-")
	if err != nil {
		return "", "", nil, err
	}
	cleanup = func() { os.RemoveAll(tmp) }
	what := "baixando " + siShowURL(src.URL)
	if src.Ref != "" {
		what += " @" + src.Ref
	}
	if sub != "" {
		what += " (subpasta " + sub + ")"
	}
	siSay(ctx, what)
	repo := filepath.Join(tmp, "repo")
	clone := func(extra ...string) error {
		args := append([]string{"clone", "--depth", "1", "--quiet"}, extra...)
		if src.Ref != "" {
			args = append(args, "--branch", src.Ref)
		}
		args = append(args, "--", src.URL, repo)
		_, err := siGit(ctx, tmp, args...)
		return err
	}
	done := false
	if sub != "" {
		err := clone("--filter=blob:none", "--sparse", "--no-checkout")
		if err == nil {
			if _, err = siGit(ctx, repo, "sparse-checkout", "set", "--no-cone", "/"+sub+"/"); err == nil {
				_, err = siGit(ctx, repo, "checkout", "--quiet")
			}
			done = err == nil
		}
		if err != nil && (ctx.Err() != nil || siFatalClone(err)) {
			cleanup()
			return "", "", nil, siGitErr(err, src)
		}
		if !done {
			os.RemoveAll(repo) // old git or a server without partial clone: full shallow clone
		}
	}
	if !done {
		if err := clone(); err != nil {
			cleanup()
			return "", "", nil, siGitErr(err, src)
		}
	}
	commit, _ = siGit(ctx, repo, "rev-parse", "--short", "HEAD")
	base = repo
	if sub != "" {
		base = filepath.Join(repo, filepath.FromSlash(sub))
		if !pathIn(base, repo) {
			cleanup()
			return "", "", nil, errors.New("subpasta fora do repositório")
		}
		if fi, err := os.Stat(base); err != nil || !fi.IsDir() {
			cleanup()
			return "", "", nil, fmt.Errorf("a subpasta %s não existe em %s", sub, siShowURL(src.URL))
		}
	}
	return base, commit, cleanup, nil
}

// siFatalClone: a clone error a retry without partial clone won't fix (the
// repo or ref isn't there, no network): no second attempt.
func siFatalClone(err error) bool {
	m := strings.ToLower(err.Error())
	for _, s := range []string{"could not read username", "repository not found", "authentication failed", "terminal prompts disabled",
		"does not appear to be a git repository", "could not read from remote repository", "could not resolve host",
		"remote branch", "couldn't find remote ref", "permission denied"} {
		if strings.Contains(m, s) {
			return true
		}
	}
	return false
}

// siFind lists the skills under base: base itself, or every SKILL.md up to
// four levels down (a repo that bundles many skills).
func siFind(base string) ([]siFound, error) {
	var dirs []string
	if fi, err := os.Lstat(filepath.Join(base, "SKILL.md")); err == nil && fi.Mode().IsRegular() {
		dirs = []string{base}
	} else {
		_ = filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				rel, _ := filepath.Rel(base, p)
				if d.Name() == ".git" || d.Name() == "node_modules" || strings.Count(filepath.ToSlash(rel), "/") >= 4 {
					return filepath.SkipDir
				}
				return nil
			}
			if d.Name() == "SKILL.md" && d.Type().IsRegular() {
				dirs = append(dirs, filepath.Dir(p))
			}
			return nil
		})
	}
	if len(dirs) == 0 {
		return nil, errors.New("nenhum SKILL.md encontrado (uma skill é uma pasta com SKILL.md)")
	}
	var out []siFound
	for _, d := range dirs {
		name, desc := readFrontmatter(filepath.Join(d, "SKILL.md"))
		if name == "" {
			name = filepath.Base(d)
		}
		f := siFound{Name: siName(name), Desc: desc, Dir: d}
		_ = filepath.WalkDir(d, func(p string, e fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if e.IsDir() {
				if e.Name() == ".git" || e.Name() == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			if !e.Type().IsRegular() {
				return nil
			}
			fi, err := e.Info()
			if err != nil {
				return nil
			}
			f.Files++
			f.Bytes += fi.Size()
			rel, _ := filepath.Rel(d, p)
			ext := strings.ToLower(filepath.Ext(p))
			if ext == ".py" || ext == ".sh" || ext == ".js" || ext == ".mjs" || ext == ".ts" || ext == ".rb" || ext == ".pl" || fi.Mode()&0o111 != 0 {
				f.Scripts = append(f.Scripts, filepath.ToSlash(rel))
			}
			return nil
		})
		// the ones SKILL.md tells to run live in scripts/ or at the top: those first
		sort.SliceStable(f.Scripts, func(i, j int) bool {
			ri, rj := siScriptRank(f.Scripts[i]), siScriptRank(f.Scripts[j])
			if ri != rj {
				return ri < rj
			}
			return f.Scripts[i] < f.Scripts[j]
		})
		out = append(out, f)
	}
	return out, nil
}

// siName: a safe folder name for a skill.
func siName(n string) string {
	n = siNameRE.R().ReplaceAllString(strings.ToLower(strings.TrimSpace(n)), "-")
	n = strings.Trim(n, ".-")
	if n == "" {
		n = "skill"
	}
	return n
}

// siCopy copies regular files only (symlinks are skipped: they could point
// anywhere), without .git, within the size limits.
func siCopy(src, dst string) error {
	files, bytes := 0, int64(0)
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		t := filepath.Join(dst, rel)
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return os.MkdirAll(t, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		files++
		bytes += fi.Size()
		if files > siMaxFiles || bytes > siMaxBytes {
			return fmt.Errorf("skill grande demais (mais de %d arquivos ou %d MB)", siMaxFiles, siMaxBytes>>20)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(t, b, fi.Mode().Perm()&0o755|0o600)
	})
}

// siSummary is what the user sees before saying yes.
func siSummary(fs []siFound, dest string) string {
	var b strings.Builder
	for _, f := range fs {
		fmt.Fprintf(&b, "• %s — %s\n", f.Name, oneLine(f.Desc, 140))
		fmt.Fprintf(&b, "  %d arquivos, %d KB → %s\n", f.Files, (f.Bytes+1023)/1024, filepath.Join(dest, f.Name))
		if len(f.Scripts) > 0 {
			s := f.Scripts
			more := ""
			if len(s) > 6 {
				s, more = s[:6], fmt.Sprintf(" (+%d)", len(f.Scripts)-6)
			}
			fmt.Fprintf(&b, "  scripts que ela pode mandar rodar: %s%s\n", strings.Join(s, ", "), more)
		}
	}
	b.WriteString("Uma skill traz instruções e scripts de terceiros: instale só de quem você confia (cada comando ainda passa pelas permissões do jazz).")
	return b.String()
}

// siInstall installs every skill found in arg into dest. confirm (nil = yes)
// sees the summary. force replaces an existing skill of the same name.
func siInstall(ctx context.Context, arg, dest string, force bool, confirm func(summary string) bool) ([]siFound, error) {
	src, err := siParse(arg)
	if err != nil {
		return nil, err
	}
	base, commit, cleanup, err := siFetch(ctx, src)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	found, err := siFind(base)
	if err != nil {
		return nil, err
	}
	for _, f := range found {
		if _, err := os.Stat(filepath.Join(dest, f.Name)); err == nil && !force {
			return nil, fmt.Errorf("a skill %s já está instalada em %s — use `jazz skill update %s` ou --force", f.Name, filepath.Join(dest, f.Name), f.Name)
		}
	}
	if confirm != nil && !confirm(siSummary(found, dest)) {
		return nil, errors.New("instalação cancelada")
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return nil, err
	}
	for _, f := range found {
		final := filepath.Join(dest, f.Name)
		stage := final + ".installing"
		os.RemoveAll(stage)
		if err := siCopy(f.Dir, stage); err != nil {
			os.RemoveAll(stage)
			return nil, err
		}
		sub := src.Sub
		if rel, err := filepath.Rel(base, f.Dir); err == nil && rel != "." {
			sub = strings.Trim(filepath.ToSlash(filepath.Join(src.Sub, rel)), "/")
		}
		rec := siOriginRec{Source: arg, URL: src.URL, Ref: src.Ref, Sub: sub, Commit: commit, Installed: time.Now().Format(time.RFC3339)}
		if src.Local != "" {
			rec.Source = src.Local
		}
		b, _ := json.MarshalIndent(rec, "", "  ")
		_ = os.WriteFile(filepath.Join(stage, siOrigin), b, 0o644)
		old := final + ".old"
		os.RemoveAll(old)
		if _, err := os.Stat(final); err == nil {
			if err := os.Rename(final, old); err != nil {
				os.RemoveAll(stage)
				return nil, err
			}
		}
		if err := os.Rename(stage, final); err != nil {
			_ = os.Rename(old, final)
			return nil, err
		}
		os.RemoveAll(old)
		f.Dir = final
	}
	for i := range found {
		found[i].Dir = filepath.Join(dest, found[i].Name)
	}
	return found, nil
}

func siUserDir() string { return filepath.Join(configDir(), "skills") }

// siDest: the user dir, or the project's .jazz/skills.
func siDest(root string, project bool) string {
	if project {
		return filepath.Join(root, ".jazz", "skills")
	}
	return siUserDir()
}

// siInstalled lists installed skills with their origin (user and project).
func siInstalled(root string) []string {
	var out []string
	for _, d := range []string{siUserDir(), filepath.Join(root, ".jazz", "skills")} {
		ents, _ := os.ReadDir(d)
		for _, e := range ents {
			if !e.IsDir() || strings.HasSuffix(e.Name(), ".installing") || strings.HasSuffix(e.Name(), ".old") {
				continue
			}
			p := filepath.Join(d, e.Name())
			_, desc := readFrontmatter(filepath.Join(p, "SKILL.md"))
			from := "local"
			if b, err := os.ReadFile(filepath.Join(p, siOrigin)); err == nil {
				var r siOriginRec
				if json.Unmarshal(b, &r) == nil {
					from = r.Source
					if r.Commit != "" {
						from += " @" + r.Commit
					}
				}
			}
			out = append(out, fmt.Sprintf("%-22s %s\n%-22s   %s · %s", e.Name(), oneLine(desc, 90), "", from, p))
		}
	}
	return out
}

// cmdSkill: jazz skill install|list|rm|update.
func cmdSkill(root string, args []string, in io.Reader, out io.Writer) error {
	usage := errors.New("uso: jazz skill install <url|dono/repo|pasta> [--project] [--yes] [--force] · jazz skill list · jazz skill rm <nome> · jazz skill update <nome>")
	if len(args) == 0 {
		return usage
	}
	var pos []string
	project, yes, force := false, false, false
	for _, a := range args[1:] {
		switch a {
		case "--project", "-p":
			project = true
		case "--yes", "-y":
			yes = true
		case "--force", "-f":
			force = true
		default:
			pos = append(pos, a)
		}
	}
	// Ctrl+C / kill / closed terminal cancel git and remove the temp clone
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	ctx = siWithProgress(ctx, func(m string) { fmt.Fprintln(out, m+"…") })
	switch args[0] {
	case "install", "add", "i":
		if len(pos) != 1 {
			return usage
		}
		var confirm func(string) bool
		if !yes {
			r := bufio.NewReader(in)
			confirm = func(s string) bool {
				fmt.Fprintln(out, s)
				fmt.Fprint(out, "Instalar? [s/N] ")
				l, _ := r.ReadString('\n')
				l = strings.ToLower(strings.TrimSpace(l))
				return l == "s" || l == "sim" || l == "y" || l == "yes"
			}
		}
		found, err := siInstall(ctx, pos[0], siDest(root, project), force, confirm)
		if err != nil {
			return err
		}
		for _, f := range found {
			fmt.Fprintf(out, "instalada: %s → %s\n", f.Name, f.Dir)
		}
		fmt.Fprintln(out, "o jazz lista a skill no próximo turno; peça pelo nome ou use /skill")
		return nil
	case "list", "ls":
		l := siInstalled(root)
		if len(l) == 0 {
			fmt.Fprintln(out, "nenhuma skill instalada (jazz skill install <url>)")
			return nil
		}
		for _, s := range l {
			fmt.Fprintln(out, s)
		}
		return nil
	case "rm", "remove", "uninstall":
		if len(pos) != 1 {
			return usage
		}
		name := siName(pos[0])
		for _, d := range []string{siDest(root, project), siDest(root, !project)} {
			p := filepath.Join(d, name)
			if _, err := os.Stat(filepath.Join(p, "SKILL.md")); err == nil {
				if err := os.RemoveAll(p); err != nil {
					return err
				}
				fmt.Fprintln(out, "removida:", p)
				return nil
			}
		}
		return fmt.Errorf("skill %s não está instalada", name)
	case "update", "up":
		if len(pos) != 1 {
			return usage
		}
		name := siName(pos[0])
		for _, d := range []string{siDest(root, project), siDest(root, !project)} {
			b, err := os.ReadFile(filepath.Join(d, name, siOrigin))
			if err != nil {
				continue
			}
			var r siOriginRec
			if json.Unmarshal(b, &r) != nil || r.Source == "" {
				break
			}
			found, err := siInstall(ctx, r.Source, d, true, nil)
			if err != nil {
				return err
			}
			for _, f := range found {
				fmt.Fprintf(out, "atualizada: %s → %s\n", f.Name, f.Dir)
			}
			return nil
		}
		return fmt.Errorf("skill %s não tem origem registrada (instale de novo com jazz skill install <url> --force)", name)
	}
	return usage
}

// ---------- skills named in the chat ----------

var (
	siChatURLRE  = lazyRx(`(?i)(https?://(?:github\.com|gitlab\.com|codeberg\.org)/[\w.-]+/[\w.-]+(?:/(?:-/)?(?:tree|blob)/[^\s)>\]]+)?)`)
	siSkillWord  = lazyRx(`(?i)\b(skills?|habilidades?)\b`)
	siInstallVRB = lazyRx(`(?i)\b(instal\w*|install\w*|adicion\w*|add)\b`)
)

// siChatURL: a forge URL in a message that talks about a skill ("" = none).
func siChatURL(text string) string {
	t := acStripReminders(text)
	if !siSkillWord.MatchString(t) {
		return ""
	}
	return strings.TrimRight(siChatURLRE.FindString(t), ".,;:!?")
}

// siInstalledFrom: the installed skill dir whose origin is url ("" = none).
func siInstalledFrom(root, url string) string {
	want, err := siParse(url)
	if err != nil {
		return ""
	}
	for _, d := range []string{siUserDir(), filepath.Join(root, ".jazz", "skills")} {
		ents, _ := os.ReadDir(d)
		for _, e := range ents {
			b, err := os.ReadFile(filepath.Join(d, e.Name(), siOrigin))
			if err != nil {
				continue
			}
			var r siOriginRec
			if json.Unmarshal(b, &r) == nil && r.URL != "" && strings.EqualFold(r.URL, want.URL) && (want.Sub == "" || r.Sub == want.Sub) {
				return filepath.Join(d, e.Name())
			}
		}
	}
	return ""
}

// siNote tells the model where installed skills live. fresh: jazz installed
// them just now because the user asked, so the model must not tell the user
// they "were already installed".
func siNote(found []siFound, fresh bool) string {
	var notes []string
	for _, f := range found {
		notes = append(notes, fmt.Sprintf("- %s: %s (read %s first; relative paths and <skill-dir> in it mean %s)", f.Name, oneLine(f.Desc, 160), filepath.Join(f.Dir, "SKILL.md"), f.Dir))
	}
	head := "These skills are installed for this request:"
	if fresh {
		head = "jazz installed these skills just now, at the user's request in this message (they were NOT installed before; if you mention it, say it was installed now):"
	}
	return "<system-reminder>" + head + "\n" + strings.Join(notes, "\n") + "\nRead the SKILL.md and follow it exactly (its golden path, in order); run its scripts by their full path in that folder, from the project directory. Do not hand-write replacements for its tools.</system-reminder>"
}

// siHeadlessTurn: with -p, a request that explicitly says to install a skill
// from a URL installs it (no prompt to answer); a skill already installed
// from that URL is pointed at. Returns the note for the model ("" = none).
func siHeadlessTurn(root, prompt string, errw io.Writer) string {
	url := siChatURL(prompt)
	if url == "" {
		return ""
	}
	if d := siInstalledFrom(root, url); d != "" {
		name, desc := readFrontmatter(filepath.Join(d, "SKILL.md"))
		return siNote([]siFound{{Name: name, Desc: desc, Dir: d}}, false)
	}
	if !siInstallVRB.MatchString(acStripReminders(prompt)) {
		return "<system-reminder>The user mentions the skill " + url + ", which is not installed. To install it the user runs `jazz skill install " + url + "` (jazz's skills folder is not writable for you). Meanwhile you may clone it into the project and follow its SKILL.md from there.</system-reminder>"
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	ctx = siWithProgress(ctx, func(m string) { fmt.Fprintln(errw, "jazz: "+m+"…") })
	found, err := siInstall(ctx, url, siUserDir(), false, nil)
	if err != nil {
		fmt.Fprintln(errw, "jazz: skill não instalada:", err)
		return ""
	}
	for _, f := range found {
		fmt.Fprintf(errw, "jazz: skill instalada: %s → %s\n", f.Name, f.Dir)
	}
	return siNote(found, true)
}

func siScriptRank(p string) int {
	switch {
	case strings.HasPrefix(p, "scripts/") || strings.HasPrefix(p, "bin/"):
		return 0
	case !strings.Contains(p, "/"):
		return 1
	}
	return 2
}
