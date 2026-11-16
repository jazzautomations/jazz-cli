package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// A setup script in a repository the user explicitly asked to use (its URL
// is in the user's own messages and the clone's origin matches) is not a
// hostile build target: "downloads and runs code" is what setup scripts do.
// It becomes a question every time (also in yolo, never remembered); other
// CRITICAL findings (secret egress, deleting home, dangerous commands) and
// any repository the user did not name stay hard blocks. Eval/demo on
// 12/11: video-forge's setup_environment.py was a dead end.

var urRepoURLRE = lazyRx(`(?i)(?:https?://|git@)(github\.com|gitlab\.com|codeberg\.org|bitbucket\.org)[/:]([\w.-]+)/([\w.-]+?)(?:\.git)?(?:[/?#\s]|$)`)

// urSlug normalizes host/owner/repo of a git URL ("" = not a known forge URL).
func urSlug(u string) string {
	m := urRepoURLRE.FindStringSubmatch(strings.TrimSpace(u) + " ")
	if m == nil {
		return ""
	}
	return strings.ToLower(m[1] + "/" + m[2] + "/" + strings.TrimSuffix(m[3], ".git"))
}

// urUserRepos: forge repositories named in the user's own messages.
func urUserRepos(msgs []Message) map[string]bool {
	out := map[string]bool{}
	for _, m := range msgs {
		if m.Role != "user" {
			continue
		}
		for _, x := range urRepoURLRE.FindAllString(acStripReminders(m.Content)+" ", -1) {
			if s := urSlug(x); s != "" {
				out[s] = true
			}
		}
	}
	return out
}

// urScriptFromCrit: the script path of a build-target finding
// ("script a/b.py: why") relative to root ("" = not a script finding).
func urScriptFromCrit(crit string) string {
	if !strings.HasPrefix(crit, "script ") {
		return ""
	}
	p, _, ok := strings.Cut(strings.TrimPrefix(crit, "script "), ": ")
	if !ok {
		return ""
	}
	return p
}

// urRequestedRepo returns the repo slug when the CRITICAL finding is only a
// "downloads and runs code" in a script whose git origin the user named.
func urRequestedRepo(ctx context.Context, root, crit string, msgs []Message) string {
	if !strings.Contains(crit, "baixa e executa código") {
		return ""
	}
	rel := urScriptFromCrit(crit)
	if rel == "" {
		return ""
	}
	want := urUserRepos(msgs)
	if len(want) == 0 {
		return ""
	}
	dir := filepath.Dir(filepath.Join(root, filepath.FromSlash(rel)))
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	b, err := exec.CommandContext(cctx, "git", "-C", dir, "remote", "get-url", "origin").Output()
	if err != nil {
		return ""
	}
	if s := urSlug(string(b)); s != "" && want[s] {
		return s
	}
	return ""
}

// ---------- installed skills are not the model's to rewrite ----------

// urSkillDirs: where installed skills live outside the project (they run in
// every session, so a change there is persistence, not a project edit).
func urSkillDirs() []string {
	d := []string{siUserDir()}
	if h, _ := os.UserHomeDir(); h != "" {
		d = append(d, filepath.Join(h, ".claude", "skills"), filepath.Join(h, ".agents", "skills"), filepath.Join(h, ".codex", "skills"))
	}
	return d
}

// urSkillWrite: the installed-skill file a call would change ("" = none).
func (a *Agent) urSkillWrite(tool string, args map[string]any, cmd string) string {
	var targets []string
	switch tool {
	case "write", "edit":
		if p, _ := argStr(args, "path"); strings.TrimSpace(p) != "" {
			targets = append(targets, a.tools.abs(p))
		}
	case "bash":
		w, _ := hgShellTargets(cmd)
		for _, p := range w {
			targets = append(targets, a.tools.abs(p))
		}
	}
	for _, t := range targets {
		rp := resolvePath(t)
		for _, d := range urSkillDirs() {
			if pathIn(rp, resolvePath(d)) {
				return t
			}
		}
	}
	return ""
}
