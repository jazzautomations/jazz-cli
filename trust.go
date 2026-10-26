package main

// trust.go: one-time trust for project-level config that can run code or widen
// permissions (.mcp.json servers, project hooks, project allow rules). Owner: platform.
// Contract surface (CONTRACT.md): IsTrusted, Trust.
//
// A cloned repository must not be able to run commands, exfiltrate keys or
// auto-approve tools just because the user opened jazz in it (bug 2). Every
// such file is hashed (sha256) and the hash the user approved is stored per
// absolute project root in configDir()/trusted.json (0600). When the content
// changes, the user is asked again. Headless runs never ask: untrusted project
// config is skipped with a warning on stderr.
//
// The hash covers what actually runs (security review F3): when a hook
// command, an MCP server's command/args or a project script names a file
// inside the project (./scripts/lint.sh, tools/mcp.js), that file's sha256 is
// part of the trusted content, so a `git pull` or an edit by the model that
// changes the script asks again. Project hooks re-check the store before every
// run (hooks.go).
//
// Project steering rules (.jazz/steering, .kiro/steering, .cursor/rules; kind
// "steering", see steering.go) are gated too: they reach the model with system
// authority, some of them in the middle of a turn.
//
// Folder trust (F1, kinds "folder" and "instructions"): the first time jazz
// runs in a folder it asks whether the folder is trusted, listing what will be
// loaded (AGENTS.md/CLAUDE.md, .jazz/*, .mcp.json, skills, commands). Until it
// is, the interactive session runs in ask mode and headless runs deny bash
// commands that are not read-only (unless --allow-tools rules or --yolo say
// otherwise). The instruction files are hashed too: a changed AGENTS.md or a
// new skill asks again. Deny/ask rules from the project are always honoured,
// because they can only make jazz more careful.

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

var pfTrustMu sync.Mutex

func pfTrustPath() string { return filepath.Join(configDir(), "trusted.json") }

// pfTrustRoot normalises a project root (absolute, symlinks resolved).
func pfTrustRoot(root string) string {
	if a, err := filepath.Abs(root); err == nil {
		root = a
	}
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	return root
}

func pfHash(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func pfLoadTrust() map[string]map[string]string {
	out := map[string]map[string]string{}
	b, err := os.ReadFile(pfTrustPath())
	if err == nil {
		_ = json.Unmarshal(b, &out)
	}
	return out
}

func pfSaveTrust(all map[string]map[string]string) error {
	b, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(configDir(), 0o700); err != nil {
		return err
	}
	return writeFileAtomic(pfTrustPath(), b, 0o600)
}

// IsTrusted reports whether the user already trusted this exact content
// (sha256 of content) of kind ("mcp" | "hooks" | …) for project root.
// Empty content needs no trust.
func IsTrusted(root, kind string, content []byte) bool {
	if len(content) == 0 {
		return true
	}
	pfTrustMu.Lock()
	defer pfTrustMu.Unlock()
	r, h := pfTrustRoot(root), pfHash(content)
	if pfSessTrust[r+"\x00"+kind] == h {
		return true
	}
	return pfLoadTrust()[r][kind] == h
}

// pfSessTrust is trust granted for this process only ("só nesta sessão" in
// the startup question): root \x00 kind -> sha256. Guarded by pfTrustMu.
var pfSessTrust = map[string]string{}

// pfTrustSession trusts the snapshot st until jazz exits (nothing is written).
func pfTrustSession(root string, st pfTrustState) {
	pfTrustMu.Lock()
	defer pfTrustMu.Unlock()
	r := pfTrustRoot(root)
	for _, k := range st.kinds {
		if b := st.blobs[k]; len(b) > 0 {
			pfSessTrust[r+"\x00"+k] = pfHash(b)
		}
	}
	auditRec("trust", map[string]any{"action": "grant_session", "root": r, "kinds": st.kinds, "ok": true}) // audit.go
}

// Trust records the user's approval of content for root/kind.
func Trust(root, kind string, content []byte) error {
	pfTrustMu.Lock()
	defer pfTrustMu.Unlock()
	all := pfLoadTrust()
	r := pfTrustRoot(root)
	if all[r] == nil {
		all[r] = map[string]string{}
	}
	act, h := "grant", ""
	if len(content) == 0 {
		delete(all[r], kind)
		act = "revoke"
	} else {
		h = pfHash(content)
		all[r][kind] = h
	}
	err := pfSaveTrust(all)
	auditRec("trust", map[string]any{"action": act, "root": r, "kinds": []string{kind}, "content_sha": h, "ok": err == nil}) // audit.go
	return err
}

// pfUntrust forgets every approval for root (/trust revoke, jazz trust --revoke).
func pfUntrust(root string) error {
	pfTrustMu.Lock()
	defer pfTrustMu.Unlock()
	all := pfLoadTrust()
	delete(all, pfTrustRoot(root))
	for k := range pfSessTrust {
		if strings.HasPrefix(k, pfTrustRoot(root)+"\x00") {
			delete(pfSessTrust, k)
		}
	}
	err := pfSaveTrust(all)
	auditRec("trust", map[string]any{"action": "revoke_all", "root": pfTrustRoot(root), "ok": err == nil}) // audit.go
	return err
}

// ---------- safe reads of project files (F6) ----------

// pfInsideRoot reports whether p, with every symlink resolved, is inside root.
func pfInsideRoot(p, root string) bool {
	if root == "" {
		return false
	}
	rr := pfTrustRoot(root)
	rp, err := filepath.EvalSymlinks(p)
	if err != nil {
		return false
	}
	return pathUnder(rp, rr)
}

// pfSafeRead reads a project file that jazz injects into the prompt or runs:
// it must be a regular file (no symlink), at most max bytes, and — when root
// is set — really inside the project after symlinks in its directories are
// resolved. AGENTS.md -> /proc/self/environ or a skills dir symlinked to ~/.ssh
// are refused.
func pfSafeRead(p, root string, max int64) ([]byte, bool) {
	fi, err := os.Lstat(p)
	if err != nil || !fi.Mode().IsRegular() || (max > 0 && fi.Size() > max) {
		return nil, false
	}
	if root != "" && !pfInsideRoot(p, root) {
		return nil, false
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, false
	}
	return b, true
}

// ---------- files referenced by hooks, MCP servers and scripts (F3) ----------

// reRefFile finds words that name a file: a relative ./ or ../ path, a
// $CLAUDE_PROJECT_DIR/… path, or anything ending in a script extension.
var reRefFile = lazyRx(`(?:\$\{?(?:CLAUDE_PROJECT_DIR|JAZZ_PROJECT_DIR|PWD)\}?/|\.{1,2}/)[A-Za-z0-9_@%+=,.\-/]+|[A-Za-z0-9_@%+=,.\-/]+\.(?:sh|bash|zsh|fish|py|js|mjs|cjs|ts|mts|cts|rb|pl|php|lua|ps1|jar|exe)\b`)

const pfRefMax = 4 << 20

// pfRefFiles returns "rel-path sha256" for every file named in text that
// exists inside root (relative names resolve against base, default root).
func pfRefFiles(root, base, text string) []string {
	if root == "" || text == "" {
		return nil
	}
	if base == "" {
		base = root
	} else if !filepath.IsAbs(base) {
		base = filepath.Join(root, base)
	}
	seen := map[string]bool{}
	var out []string
	for _, m := range reRefFile.FindAllString(text, 64) {
		for _, pre := range []string{"${CLAUDE_PROJECT_DIR}/", "$CLAUDE_PROJECT_DIR/", "${JAZZ_PROJECT_DIR}/", "$JAZZ_PROJECT_DIR/", "${PWD}/", "$PWD/"} {
			if strings.HasPrefix(m, pre) {
				m = filepath.Join(root, m[len(pre):])
			}
		}
		p := m
		if !filepath.IsAbs(p) {
			p = filepath.Join(base, p)
		}
		p = filepath.Clean(p)
		if seen[p] {
			continue
		}
		seen[p] = true
		fi, err := os.Stat(p)
		if err != nil || !fi.Mode().IsRegular() || !pfInsideRoot(p, root) {
			continue
		}
		var data []byte
		if fi.Size() <= pfRefMax {
			data, _ = os.ReadFile(p)
		} else {
			data = []byte(fmt.Sprintf("size %d mtime %d", fi.Size(), fi.ModTime().UnixNano()))
		}
		rel, err := filepath.Rel(pfTrustRoot(root), pfTrustRoot(p))
		if err != nil {
			rel = p
		}
		out = append(out, filepath.ToSlash(rel)+" "+pfHash(data))
	}
	sort.Strings(out)
	return out
}

// pfRefSummary renders referenced files for the trust prompt.
func pfRefSummary(refs []string, indent string) string {
	var b strings.Builder
	for _, r := range refs {
		name, sum, _ := strings.Cut(r, " ")
		if len(sum) > 12 {
			sum = sum[:12]
		}
		fmt.Fprintf(&b, "%sarquivo executado: %s (sha256 %s…)\n", indent, visibleDetail(name), sum)
	}
	return b.String()
}

// ---------- project files that need trust ----------

// pfProjectMCP returns the raw project .mcp.json ("" when absent).
func pfProjectMCP(root string) []byte {
	b, err := os.ReadFile(filepath.Join(root, ".mcp.json"))
	if err != nil {
		return nil
	}
	return b
}

// pfProjectMCPServers parses the project .mcp.json (nil when absent/invalid).
func pfProjectMCPServers(raw []byte) (map[string]MCPServerConf, bool) {
	var f struct {
		MCPServers map[string]MCPServerConf `json:"mcpServers"`
	}
	if json.Unmarshal(raw, &f) != nil {
		return nil, false
	}
	return f.MCPServers, true
}

// pfMCPRefs lists the project files the project's MCP servers run.
func pfMCPRefs(root string, raw []byte) []string {
	servers, _ := pfProjectMCPServers(raw)
	var out []string
	names := make([]string, 0, len(servers))
	for n := range servers {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		c := servers[n]
		if c.URL != "" && c.Command == "" {
			continue
		}
		out = append(out, pfRefFiles(root, c.Cwd, c.Command+" "+strings.Join(c.Args, " "))...)
	}
	return out
}

// pfProjectMCPBlob is what kind "mcp" hashes: the .mcp.json bytes plus the
// files its servers run. Without referenced files it is the raw JSON (so
// earlier approvals stay valid).
func pfProjectMCPBlob(root string) []byte {
	raw := pfProjectMCP(root)
	if raw == nil {
		return nil
	}
	refs := pfMCPRefs(root, raw)
	if len(refs) == 0 {
		return raw
	}
	return append(append(append([]byte(nil), raw...), "\n== files\n"...), strings.Join(refs, "\n")...)
}

// pfProjectMCPTrusted reports whether the project's MCP servers may start.
func pfProjectMCPTrusted(root string) bool {
	return IsTrusted(root, "mcp", pfProjectMCPBlob(root))
}

// pfSettings is the part of a Claude Code style settings.json jazz reads.
type pfSettings struct {
	Permissions Permissions              `json:"permissions"`
	Hooks       map[string][]HookMatcher `json:"hooks"`
}

// pfProjectSettingsFiles lists the project settings files, most specific last.
func pfProjectSettingsFiles(root string) []string {
	return []string{
		filepath.Join(root, ".claude", "settings.json"),
		filepath.Join(root, ".claude", "settings.local.json"),
		filepath.Join(root, ".jazz", "settings.json"),
	}
}

// pfGlobalSettingsFiles lists user-level settings files (always trusted).
func pfGlobalSettingsFiles() []string {
	home, _ := os.UserHomeDir()
	if home == "" {
		return nil
	}
	return []string{filepath.Join(home, ".claude", "settings.json")}
}

func pfReadSettings(p string) (pfSettings, bool) {
	var s pfSettings
	b, err := os.ReadFile(p)
	if err != nil {
		return s, false
	}
	if json.Unmarshal(b, &s) != nil {
		return s, false
	}
	return s, true
}

// pfHookRefs lists the project files the hooks in s run.
func pfHookRefs(root string, s pfSettings) []string {
	var cmds []string
	for _, list := range s.Hooks {
		for _, m := range list {
			for _, h := range m.Hooks {
				cmds = append(cmds, h.Command)
			}
		}
	}
	sort.Strings(cmds)
	return pfRefFiles(root, "", strings.Join(cmds, "\n"))
}

// pfProjectPolicy is the trust-relevant part of the project settings: hooks
// (they run commands), the files those hooks run, and allow rules (they skip
// prompts). Its canonical JSON is what kind "hooks" hashes.
func pfProjectPolicy(root string) []byte {
	type part struct {
		File  string                   `json:"file"`
		Hooks map[string][]HookMatcher `json:"hooks,omitempty"`
		Allow []string                 `json:"allow,omitempty"`
		Files []string                 `json:"files,omitempty"`
	}
	var parts []part
	for _, p := range pfProjectSettingsFiles(root) {
		s, ok := pfReadSettings(p)
		if !ok || (len(s.Hooks) == 0 && len(s.Permissions.Allow) == 0) {
			continue
		}
		rel, _ := filepath.Rel(root, p)
		parts = append(parts, part{File: rel, Hooks: s.Hooks, Allow: s.Permissions.Allow, Files: pfHookRefs(root, s)})
	}
	if len(parts) == 0 {
		return nil
	}
	b, _ := json.Marshal(parts)
	return b
}

// pfProjectPolicyTrusted reports whether project hooks/allow rules may be used.
func pfProjectPolicyTrusted(root string) bool {
	return IsTrusted(root, "hooks", pfProjectPolicy(root))
}

// pfScriptsBlob is what kind "scripts" hashes: the .jazz/scripts files and
// intents plus any other project file their commands name.
func pfScriptsBlob(root string) []byte {
	b := scProjectBlob(root)
	if len(b) == 0 {
		return b
	}
	if refs := pfRefFiles(root, "", string(b)); len(refs) > 0 {
		b = append(append(b, "\n== files\n"...), strings.Join(refs, "\n")...)
	}
	return b
}

// ---------- folder trust: instructions, skills, commands (F1) ----------

// pfFolderMark is the content of kind "folder": the folder itself was trusted.
var pfFolderMark = []byte("folder-trusted-v1")

// pfInstructionFiles lists the project files that steer the model as text:
// AGENTS.override.md/AGENTS.md/CLAUDE.md/JAZZ.md from the git root down to
// root, and the project's skills and slash commands.
func pfInstructionFiles(root string) []string {
	var out []string
	rr := pfTrustRoot(root)
	top := gitRoot(rr)
	if top != "" {
		top = pfTrustRoot(top)
	}
	if top == "" || !pathUnder(rr, top) {
		top = rr
	}
	for d := rr; ; d = filepath.Dir(d) {
		for _, n := range []string{"AGENTS.override.md", "AGENTS.md", "CLAUDE.md", "JAZZ.md"} {
			if p := filepath.Join(d, n); pfExists(p) {
				out = append(out, p)
			}
		}
		if d == top || d == filepath.Dir(d) {
			break
		}
	}
	for _, sd := range []string{filepath.Join(rr, ".jazz", "skills"), filepath.Join(rr, ".claude", "skills")} {
		ents, _ := os.ReadDir(sd)
		for _, e := range ents {
			if p := filepath.Join(sd, e.Name(), "SKILL.md"); pfExists(p) {
				out = append(out, p)
			}
		}
	}
	for _, cd := range []string{filepath.Join(rr, ".jazz", "commands"), filepath.Join(rr, ".claude", "commands")} {
		ents, _ := os.ReadDir(cd)
		for _, e := range ents {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
				out = append(out, filepath.Join(cd, e.Name()))
			}
		}
	}
	sort.Strings(out)
	return out
}

func pfExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// pfInstructionsBlob is what kind "instructions" hashes (nil = no files).
func pfInstructionsBlob(root string) []byte {
	files := pfInstructionFiles(root)
	if len(files) == 0 {
		return nil
	}
	var b []byte
	rr := pfTrustRoot(root)
	for _, p := range files {
		rel, err := filepath.Rel(rr, p)
		if err != nil {
			rel = p
		}
		b = append(b, filepath.ToSlash(rel)...)
		b = append(b, 0)
		if fi, err := os.Lstat(p); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			t, _ := os.Readlink(p)
			b = append(b, "symlink -> "+t...)
		} else if data, err := os.ReadFile(p); err == nil && len(data) <= pfRefMax {
			b = append(b, data...)
		}
		b = append(b, 0)
	}
	return b
}

// pfFolderTrusted reports whether the user trusted this folder and its
// current instruction files. Untrusted folders run in ask mode (F1).
func pfFolderTrusted(root string) bool {
	return IsTrusted(root, "folder", pfFolderMark) && IsTrusted(root, "instructions", pfInstructionsBlob(root))
}

// pfFolderSummary lists what the folder will load, for the trust prompt.
func pfFolderSummary(root string) string {
	var b strings.Builder
	rr := pfTrustRoot(root)
	files := pfInstructionFiles(root)
	if len(files) == 0 {
		b.WriteString("  (nenhum AGENTS.md, CLAUDE.md, skill ou comando do projeto)\n")
	}
	for _, p := range files {
		rel, err := filepath.Rel(rr, p)
		if err != nil {
			rel = p
		}
		what := "instruções"
		switch {
		case strings.HasSuffix(p, "SKILL.md"):
			what = "skill"
		case strings.Contains(filepath.ToSlash(p), "/commands/"):
			what = "comando"
		}
		note := ""
		if fi, err := os.Lstat(p); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			note = " (link simbólico: não será lido)"
		}
		fmt.Fprintf(&b, "  %s: %s%s\n", what, visibleDetail(filepath.ToSlash(rel)), note)
	}
	return b.String()
}

// ---------- the trust prompt ----------

// pfTrustState is a snapshot of what needs trust: the prompt shows it and
// pfTrustStore records exactly these bytes (no re-read: what was shown is
// what is trusted, review §4 test 5).
type pfTrustState struct {
	summary string
	kinds   []string
	blobs   map[string][]byte
}

// pfTrustCollect snapshots untrusted project config; folder adds the folder
// and instructions kinds (startup gate, `jazz trust`).
func pfTrustCollect(root string, folder bool) pfTrustState {
	st := pfTrustState{blobs: map[string][]byte{}}
	var b strings.Builder
	need := func(kind string, blob []byte) bool {
		if IsTrusted(root, kind, blob) {
			return false
		}
		st.kinds = append(st.kinds, kind)
		st.blobs[kind] = blob
		return true
	}
	if folder {
		fb := pfFolderMark
		ib := pfInstructionsBlob(root)
		if !IsTrusted(root, "folder", fb) {
			st.kinds = append(st.kinds, "folder")
			st.blobs["folder"] = fb
			b.WriteString("  pasta ainda não confiável. Será carregado:\n")
			b.WriteString(indent(strings.TrimRight(pfFolderSummary(root), "\n"), "  ") + "\n")
			if !IsTrusted(root, "instructions", ib) {
				st.kinds = append(st.kinds, "instructions")
				st.blobs["instructions"] = ib
			}
		} else if need("instructions", ib) {
			b.WriteString("  instruções, skills ou comandos do projeto mudaram:\n")
			b.WriteString(indent(strings.TrimRight(pfFolderSummary(root), "\n"), "  ") + "\n")
		}
	}
	raw := pfProjectMCP(root)
	if need("mcp", pfProjectMCPBlob(root)) {
		b.WriteString("  servidores MCP (.mcp.json):\n")
		servers, ok := pfProjectMCPServers(raw)
		if !ok {
			b.WriteString("    (arquivo inválido — será ignorado)\n")
		}
		var names []string
		for n := range servers {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			c := servers[n]
			what := strings.TrimSpace(c.Command + " " + strings.Join(c.Args, " "))
			if c.URL != "" {
				what = c.URL
			}
			// every string here comes from the repo: escapes are made visible so
			// a hostile name cannot conceal or erase the lines around it
			fmt.Fprintf(&b, "    %s: %s\n", visibleDetail(n), visibleDetail(what))
			if c.Cwd != "" {
				fmt.Fprintf(&b, "      cwd: %s\n", visibleDetail(c.Cwd))
			}
			for _, kv := range []struct {
				label string
				m     map[string]string
			}{{"env", c.Env}, {"header", c.Headers}} {
				var ks []string
				for k := range kv.m {
					ks = append(ks, k)
				}
				sort.Strings(ks)
				for _, k := range ks {
					fmt.Fprintf(&b, "      %s: %s=%s\n", kv.label, visibleDetail(k), visibleDetail(kv.m[k]))
				}
			}
			if c.URL == "" || c.Command != "" {
				b.WriteString(pfRefSummary(pfRefFiles(root, c.Cwd, c.Command+" "+strings.Join(c.Args, " ")), "      "))
			}
		}
	}
	if need("hooks", pfProjectPolicy(root)) {
		for _, p := range pfProjectSettingsFiles(root) {
			s, ok := pfReadSettings(p)
			if !ok {
				continue
			}
			rel, _ := filepath.Rel(root, p)
			rel = visibleDetail(rel)
			var evs []string
			for ev := range s.Hooks {
				evs = append(evs, ev)
			}
			sort.Strings(evs)
			for _, ev := range evs {
				for _, m := range s.Hooks[ev] {
					for _, h := range m.Hooks {
						fmt.Fprintf(&b, "  hook %s (%s) em %s: %s\n", visibleDetail(ev), visibleDetail(m.Matcher), rel, visibleDetail(h.Command))
					}
				}
			}
			b.WriteString(pfRefSummary(pfHookRefs(root, s), "    "))
			for _, r := range s.Permissions.Allow {
				fmt.Fprintf(&b, "  regra allow em %s: %s\n", rel, visibleDetail(r))
			}
		}
	}
	if need("steering", acProjectSteeringBlob(root)) {
		b.WriteString(acSteeringTrustSummary(root))
	}
	if sb := pfScriptsBlob(root); need("scripts", sb) { // .jazz/scripts + .jazz/intents.json
		b.WriteString(scTrustSummary(root))
		if refs := pfRefFiles(root, "", string(scProjectBlob(root))); len(refs) > 0 {
			b.WriteString(pfRefSummary(refs, "    "))
		}
	}
	if need(dgTrustKind, dgProjectBlob(root)) { // .jazz/diffrules.d (diffguard.go)
		b.WriteString(dgTrustSummary(root))
	}
	st.summary = b.String()
	return st
}

// pfTrustSummary describes untrusted project config for the trust prompt.
// kinds lists what needs trust ("mcp", "hooks", "steering", "scripts");
// empty = everything trusted. Folder trust is not included (pfTrustCollect).
func pfTrustSummary(root string) (summary string, kinds []string) {
	st := pfTrustCollect(root, false)
	return st.summary, st.kinds
}

// pfTrustStore records trust for the snapshot st.
func pfTrustStore(root string, st pfTrustState) error {
	for _, k := range st.kinds {
		if err := Trust(root, k, st.blobs[k]); err != nil {
			return err
		}
	}
	return nil
}

// pfTrustAll records trust for every kind currently present in the project
// (re-reading the files; prefer pfTrustStore with the snapshot that was shown).
func pfTrustAll(root string, kinds []string) error {
	for _, k := range kinds {
		var content []byte
		switch k {
		case "mcp":
			content = pfProjectMCPBlob(root)
		case "hooks":
			content = pfProjectPolicy(root)
		case "steering":
			content = acProjectSteeringBlob(root)
		case "scripts":
			content = pfScriptsBlob(root)
		case dgTrustKind:
			content = dgProjectBlob(root)
		case "folder":
			content = pfFolderMark
		case "instructions":
			content = pfInstructionsBlob(root)
		}
		if err := Trust(root, k, content); err != nil {
			return err
		}
	}
	return nil
}

// pfTrustGate runs before anything project-level starts. Interactive: shows the
// untrusted config once and asks [s/N] on in (a cooked terminal). Headless: prints
// a warning to w and skips it. It returns true when nothing untrusted remains.
func pfTrustGate(root string, interactive bool, in io.Reader, w io.Writer) bool {
	return pfTrustGateWith(root, false, interactive, in, w)
}

// pfTrustGateWith is pfTrustGate; folder also covers folder trust (F1).
func pfTrustGateWith(root string, folder, interactive bool, in io.Reader, w io.Writer) bool {
	st := pfTrustCollect(root, folder)
	if len(st.kinds) == 0 {
		return true
	}
	if !interactive {
		var cfgKinds []string
		folderUntrusted := false
		for _, k := range st.kinds {
			if k == "folder" || k == "instructions" {
				folderUntrusted = true
			} else {
				cfgKinds = append(cfgKinds, k)
			}
		}
		if len(cfgKinds) > 0 {
			fmt.Fprintf(w, "jazz: aviso: configuração do projeto não confiável ignorada (%s). Para confiar: jazz trust\n", strings.Join(cfgKinds, ", "))
		}
		if folderUntrusted {
			fmt.Fprintln(w, "jazz: aviso: pasta não confiável: comandos bash que não são só leitura ficam bloqueados (use --allow-tools, --yolo ou jazz trust --yes)")
		}
		return false
	}
	fmt.Fprintf(w, "\nEste projeto traz configuração que roda comandos, libera ferramentas ou dá instruções ao modelo:\n%s\n", st.summary)
	if folder {
		fmt.Fprintln(w, "Sem confiar, a sessão roda em modo ask (pede confirmação antes de mudar algo).")
	}
	fmt.Fprintln(w, "Só aceite se você confia neste repositório.")
	if cliChoose(in, w, "Confiar e ativar?", yesNo("Sim, confiar", "Não"), 0) == 0 {
		if err := pfTrustStore(root, st); err != nil {
			fmt.Fprintln(w, "jazz: não consegui salvar a confiança:", err)
		}
		return true
	}
	fmt.Fprintln(w, "Ok: a configuração do projeto fica desativada (jazz trust confia depois).")
	return false
}

// pfReadLine reads one line byte by byte (no read-ahead, so a key reader started
// later does not lose input).
func pfReadLine(in io.Reader) string {
	if f, ok := in.(*os.File); ok && !isTTY(f) {
		s, _ := bufio.NewReader(io.LimitReader(in, 4096)).ReadString('\n')
		return s
	}
	var b []byte
	one := make([]byte, 1)
	for len(b) < 4096 {
		n, err := in.Read(one)
		if n == 1 {
			if one[0] == '\n' || one[0] == '\r' {
				break
			}
			b = append(b, one[0])
		}
		if err != nil {
			break
		}
	}
	return string(b)
}

// cmdTrust implements `jazz trust [--yes] [--revoke] [pasta]`.
func cmdTrust(root string, args []string) {
	yes, revoke := false, false
	for _, a := range args {
		switch {
		case a == "--yes" || a == "-y":
			yes = true
		case a == "--revoke" || a == "revoke":
			revoke = true
		case !strings.HasPrefix(a, "-"):
			root = a
		}
	}
	root = pfTrustRoot(root)
	if revoke {
		if err := pfUntrust(root); err != nil {
			fatal(err.Error())
		}
		fmt.Println("confiança removida:", root)
		return
	}
	st := pfTrustCollect(root, true)
	if len(st.kinds) == 0 {
		fmt.Println("nada pra confiar em", root, "(pasta já confiável, sem .mcp.json, hooks, regras allow ou steering novos)")
		return
	}
	if !yes {
		if !isTTY(os.Stdin) {
			fatal("use jazz trust --yes para confiar sem perguntar")
		}
		if !pfTrustGateWith(root, true, true, os.Stdin, os.Stdout) {
			os.Exit(1)
		}
		return
	}
	fmt.Print(st.summary)
	if err := pfTrustStore(root, st); err != nil {
		fatal(err.Error())
	}
	fmt.Println("confiado:", root)
}

// pfMCPCatalogHash fingerprints what a server advertises besides tools: its
// resources, resource templates and prompts (names, URIs, descriptions,
// argument lists). Like mcpToolHash it is the rug-pull baseline: a server that
// changes any of them after the user first saw the catalog forces a new
// approval before mcp_resource / mcp_prompt run again. Resource sizes are
// left out on purpose (a growing log file is not a rug-pull); order is too.
func pfMCPCatalogHash(c mcpCatalog) string {
	type res struct{ U, N, T, D, M string }
	type tpl struct{ U, N, T, D, M string }
	type arg struct {
		N, D string
		R    bool
	}
	type prm struct {
		N, T, D string
		A       []arg
	}
	var out struct {
		R []res
		T []tpl
		P []prm
	}
	for _, r := range c.Resources {
		out.R = append(out.R, res{r.URI, r.Name, r.Title, r.Description, r.MimeType})
	}
	for _, t := range c.Templates {
		out.T = append(out.T, tpl{t.URITemplate, t.Name, t.Title, t.Description, t.MimeType})
	}
	for _, p := range c.Prompts {
		x := prm{N: p.Name, T: p.Title, D: p.Description}
		for _, a := range p.Arguments {
			x.A = append(x.A, arg{a.Name, a.Description, a.Required})
		}
		sort.Slice(x.A, func(i, j int) bool { return x.A[i].N < x.A[j].N })
		out.P = append(out.P, x)
	}
	sort.Slice(out.R, func(i, j int) bool { return out.R[i].U < out.R[j].U })
	sort.Slice(out.T, func(i, j int) bool { return out.T[i].U < out.T[j].U })
	sort.Slice(out.P, func(i, j int) bool { return out.P[i].N < out.P[j].N })
	b, _ := json.Marshal(out)
	return pfHash(b)
}
