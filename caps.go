package main

// caps.go: the capability registry (docs/design/offline-brain.md §5).
//
// Every action the offline brain can take is a registered capability with a
// schema, a risk, preconditions, effects, a dry-run flag, a verifier, a
// timeout, an undo and what it needs (OS, binaries, network). The model
// tools and the built-in scripts are wrapped as capabilities too, so the
// planner (methods.go) checks needs against the environment profile
// (envprobe.go) before it promises anything: offline → no network
// capability; a missing binary → the "command_missing" recovery (recover.go).
//
// Models declare capabilities as well (vision, audio, tools, long_context):
// the harness never sends what the model does not support; with no model
// that does, the user gets a deterministic message naming one that does.
//
// "Menor intervenção" (feedback from the first external user, who lost a
// Podman install to an agent that rewrote system configuration to start a
// container): any action that writes /etc, the kernel, system binaries,
// ~/.config/containers or Docker/Podman/WSL configuration is CRITICAL in
// every permission mode, yolo and turbo included, and a disproportionate
// action for a container task is blocked with an explanation.

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

type cbRisk int

const (
	cbLow cbRisk = iota
	cbMed
	cbHigh
	cbCritical
)

func (r cbRisk) String() string {
	return [...]string{"low", "med", "high", "critical"}[r]
}

func cbParseRisk(s string) cbRisk {
	switch strings.ToLower(s) {
	case "med", "medium":
		return cbMed
	case "high":
		return cbHigh
	case "critical":
		return cbCritical
	}
	return cbLow
}

// cbNeeds is what a capability requires of the environment.
type cbNeeds struct {
	OS   []string `json:"os,omitempty"`   // empty = any
	Bins []string `json:"bins,omitempty"` // any of these on PATH ("" = none)
	Net  bool     `json:"net,omitempty"`
}

// Capability is one registered action.
type Capability struct {
	Name     string         `json:"name"`
	Kind     string         `json:"kind"` // tool | script | brain
	Desc     string         `json:"desc"`
	Schema   map[string]any `json:"schema,omitempty"`
	Risk     cbRisk         `json:"risk"`
	Pre      []string       `json:"pre,omitempty"`
	Effects  []string       `json:"effects,omitempty"` // read | write | exec | git | net | fact:<name>
	DryRun   bool           `json:"dry_run"`
	Verifier string         `json:"verifier,omitempty"`
	Timeout  time.Duration  `json:"timeout"`
	Undo     string         `json:"undo,omitempty"`
	Needs    cbNeeds        `json:"needs"`
}

// cbToolCaps describes the model tools (tools.go) as capabilities.
var cbToolCaps = map[string]Capability{
	"read":  {Desc: "ler arquivo", Risk: cbLow, Effects: []string{"read"}, DryRun: true, Verifier: "file_exists", Timeout: 10 * time.Second},
	"write": {Desc: "escrever arquivo", Risk: cbMed, Effects: []string{"write"}, Verifier: "file_exists", Timeout: 10 * time.Second, Undo: "checkpoint"},
	"edit":  {Desc: "editar arquivo", Risk: cbMed, Effects: []string{"write"}, Verifier: "diff_nonempty", Timeout: 10 * time.Second, Undo: "checkpoint"},
	"bash":  {Desc: "comando no shell", Risk: cbHigh, Effects: []string{"exec"}, Verifier: "exit_zero", Timeout: 10 * time.Minute, Undo: "checkpoint", Needs: cbNeeds{}},
	"fetch": {Desc: "buscar URL", Risk: cbMed, Effects: []string{"net"}, DryRun: true, Timeout: 30 * time.Second, Needs: cbNeeds{Net: true}},
	"docs":  {Desc: "documentação de biblioteca", Risk: cbLow, Effects: []string{"net"}, DryRun: true, Timeout: 30 * time.Second, Needs: cbNeeds{Net: true}},
	"todo":  {Desc: "lista de tarefas", Risk: cbLow, Effects: []string{"fact:todo"}, DryRun: true, Timeout: time.Second},
}

// cbBrainCaps are the deterministic actions of the offline brain.
var cbBrainCaps = []Capability{
	{Name: "resolve_name", Desc: "resolver nome impreciso (arquivo, símbolo, host, serviço, binário)", Risk: cbLow, Effects: []string{"fact:candidates"}, DryRun: true, Verifier: "candidates_reported", Timeout: 10 * time.Second,
		Schema: map[string]any{"q": "string", "kinds": "[]string"}},
	{Name: "choose", Desc: "pergunta decisiva: até 5 opções numeradas", Risk: cbLow, Effects: []string{"fact:choice"}, DryRun: true, Timeout: 0},
	{Name: "read_file", Desc: "abrir arquivo e devolver trecho + evidência", Risk: cbLow, Effects: []string{"read"}, DryRun: true, Verifier: "file_exists+nonempty", Timeout: 10 * time.Second,
		Schema: map[string]any{"path": "string"}},
	{Name: "run_tests", Desc: "rodar o comando de teste do projeto e ler a saída", Risk: cbMed, Effects: []string{"exec"}, Verifier: "outparse", Timeout: 10 * time.Minute,
		Schema: map[string]any{"target": "string?"}},
	{Name: "git_status", Desc: "git status/branch", Risk: cbLow, Effects: []string{"read", "git"}, DryRun: true, Verifier: "exit_zero", Timeout: 20 * time.Second, Needs: cbNeeds{Bins: []string{"git"}}},
	{Name: "env_status", Desc: "perfil do ambiente (envprobe)", Risk: cbLow, Effects: []string{"fact:env"}, DryRun: true, Verifier: "profile_present", Timeout: 20 * time.Second},
	{Name: "report", Desc: "relatar ao usuário (sem efeito)", Risk: cbLow, DryRun: true, Timeout: 0},
}

// cbRegistry lists every capability: brain actions, model tools and the
// scripts of tb (nil = no scripts).
func cbRegistry(tb *Toolbox) []Capability {
	out := make([]Capability, 0, 40)
	for _, c := range cbBrainCaps {
		c.Kind = "brain"
		out = append(out, c)
	}
	names := make([]string, 0, len(cbToolCaps))
	for n := range cbToolCaps {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		c := cbToolCaps[n]
		c.Name, c.Kind = n, "tool"
		out = append(out, c)
	}
	if tb != nil {
		r := tb.scRegistry()
		for _, n := range r.names {
			d := r.defs[n]
			c := Capability{Name: "script:" + n, Kind: "script", Desc: d.Description, Schema: d.Args, Timeout: scTimeout}
			switch d.Effect {
			case "read":
				c.Risk, c.DryRun = cbLow, true
			case "write", "git":
				c.Risk, c.Undo = cbMed, "checkpoint"
			case "exec", "net":
				c.Risk = cbHigh
			}
			c.Effects = []string{d.Effect}
			if d.Effect == "net" {
				c.Needs.Net = true
			}
			c.Needs.Bins = []string{d.Run}
			out = append(out, c)
		}
	}
	return out
}

// cbFind returns the capability called name.
func cbFind(reg []Capability, name string) (Capability, bool) {
	for _, c := range reg {
		if c.Name == name {
			return c, true
		}
	}
	return Capability{}, false
}

// cbAvailable checks a capability's needs against the environment.
func cbAvailable(c Capability, p *epProfile) (bool, string) {
	if p == nil {
		return true, ""
	}
	if len(c.Needs.OS) > 0 {
		ok := false
		for _, o := range c.Needs.OS {
			ok = ok || o == p.OS
		}
		if !ok {
			return false, c.Name + " não roda em " + p.OS
		}
	}
	if c.Needs.Net && !p.Online {
		return false, c.Name + " precisa de rede e estamos offline"
	}
	if len(c.Needs.Bins) > 0 {
		have := false
		for _, b := range c.Needs.Bins {
			have = have || b == "" || epHasBin(p, b)
		}
		if !have {
			return false, c.Name + " precisa de " + strings.Join(c.Needs.Bins, " ou ") + " (não está no PATH)"
		}
	}
	return true, ""
}

// epHasBin: the binary is on the probed PATH.
func epHasBin(p *epProfile, b string) bool {
	i := sort.SearchStrings(p.PathBins, b)
	return i < len(p.PathBins) && p.PathBins[i] == b
}

// ---------- model capabilities ----------

// cbModelCaps is what a model accepts.
type cbModelCaps struct {
	Vision      bool `json:"vision"`
	VisionKnown bool `json:"vision_known"` // false = assumed (no catalog data)
	Audio       bool `json:"audio"`
	Tools       bool `json:"tools"`
	LongContext bool `json:"long_context"` // >= 100k tokens
}

// cbModelOf reads the declared capabilities of ref from the config/catalog.
func cbModelOf(cfg *Config, ref string) cbModelCaps {
	if cfg == nil || ref == "" {
		return cbModelCaps{}
	}
	ms := cfg.ModelSettings(ref)
	c := cbModelCaps{Tools: ms.Tools != "none", LongContext: ms.Context >= 100000}
	if ms.Images != nil {
		c.Vision, c.VisionKnown = *ms.Images, true
	} else {
		c.Vision = cfg.SupportsImages(ref)
	}
	// no provider in jazz takes audio input today
	return c
}

// cbNeed names what a request needs from the model.
type cbNeed struct{ Vision, Audio, LongContext bool }

// cbUnsupported returns the deterministic message for a request the model
// cannot take ("" = it can): what is missing and a model that has it.
func cbUnsupported(cfg *Config, ref string, need cbNeed) string {
	have := cbModelOf(cfg, ref)
	var miss []string
	if need.Vision && !have.Vision {
		miss = append(miss, "imagem")
	}
	if need.Audio && !have.Audio {
		miss = append(miss, "áudio")
	}
	if need.LongContext && !have.LongContext {
		miss = append(miss, "contexto longo")
	}
	if len(miss) == 0 {
		return ""
	}
	msg := fmt.Sprintf("%s não aceita %s: isso não vai ser enviado", ref, strings.Join(miss, " nem "))
	if alt := cbAlternative(cfg, ref, need); alt != "" {
		return msg + "; trocar para " + alt + "? (/model " + alt + ")"
	}
	return msg + "; nenhum modelo configurado com credencial aceita isso"
}

// cbAlternative finds a configured model (with credentials) that has the
// capabilities; one whose vision support is confirmed by the catalog wins.
func cbAlternative(cfg *Config, ref string, need cbNeed) string {
	if cfg == nil {
		return ""
	}
	best, bestKnown := "", false
	for _, p := range cfg.ProviderNames() {
		pc := cfg.Providers[p]
		if pc == nil || pc.DefaultModel == "" || !cfg.HasCreds(p) {
			continue
		}
		cand := p + "/" + pc.DefaultModel
		if cand == ref {
			continue
		}
		c := cbModelOf(cfg, cand)
		if need.Vision && !c.Vision || need.Audio && !c.Audio || need.LongContext && !c.LongContext {
			continue
		}
		known := !need.Vision || c.VisionKnown
		if best == "" || known && !bestKnown {
			best, bestKnown = cand, known
		}
	}
	return best
}

// cbAttachNotice is the call site of the request path (agent.go): images
// attached to a turn for a model without vision get the deterministic
// message; acStripImages keeps them out of the request.
func (a *Agent) cbAttachNotice(ref string) {
	if len(a.attach) == 0 || a.cfg == nil || a.ui == nil {
		return
	}
	if msg := cbUnsupported(a.cfg, ref, cbNeed{Vision: true}); msg != "" {
		a.ui.Warn(msg)
	}
}

// ---------- menor intervenção ----------

// cbSysPrefixes are the paths a task in a project never needs to write.
var cbSysPrefixes = []string{"/etc/", "/boot/", "/usr/bin/", "/usr/sbin/", "/usr/lib/", "/usr/libexec/", "/usr/local/bin/", "/usr/local/sbin/",
	"/bin/", "/sbin/", "/lib/", "/lib32/", "/lib64/", "/proc/sys/", "/sys/", "/var/lib/docker/", "/var/lib/containers/", "/var/lib/containerd/"}

// cbHomeSys are home-relative configuration of container engines and WSL.
var cbHomeSys = []string{".config/containers", ".local/share/containers", ".docker/daemon.json", ".docker/config.json", ".wslconfig"}

// cbSysPath reports whether p (as typed) is system territory and why.
func cbSysPath(p string) string {
	p = strings.Trim(p, "\"'")
	p = strings.TrimLeft(p, "<>|&")
	if p == "" {
		return ""
	}
	for _, pre := range []string{"$HOME/", "${HOME}/", "~/"} {
		if strings.HasPrefix(p, pre) {
			rest := strings.TrimPrefix(p, pre)
			for _, h := range cbHomeSys {
				if rest == h || strings.HasPrefix(rest, h+"/") {
					return "configuração de contêiner/WSL (~/" + h + ")"
				}
			}
			return ""
		}
	}
	if strings.Contains(p, "/.config/containers") || strings.Contains(p, "/.local/share/containers") || strings.HasSuffix(p, "/.wslconfig") || strings.HasSuffix(p, "/.docker/daemon.json") {
		return "configuração de contêiner/WSL (" + p + ")"
	}
	clean := p
	if strings.HasPrefix(clean, "/") {
		clean = filepath.ToSlash(filepath.Clean(clean))
	}
	for _, pre := range cbSysPrefixes {
		if clean+"/" == pre || strings.HasPrefix(clean, pre) {
			switch {
			case strings.HasPrefix(pre, "/etc/"):
				return "configuração do sistema (" + clean + ")"
			case pre == "/boot/" || pre == "/proc/sys/" || pre == "/sys/":
				return "kernel/boot (" + clean + ")"
			case strings.Contains(pre, "docker") || strings.Contains(pre, "containers"):
				return "armazenamento do motor de contêiner (" + clean + ")"
			}
			return "binários/bibliotecas do sistema (" + clean + ")"
		}
	}
	if runtime.GOOS == "windows" || strings.Contains(p, `\`) {
		lp := strings.ToLower(strings.ReplaceAll(p, `\`, "/"))
		if strings.HasPrefix(lp, "c:/windows/") || strings.HasSuffix(lp, "/.wslconfig") {
			return "configuração do sistema (" + p + ")"
		}
	}
	return ""
}

// cbWriters write their path arguments; cbDestOnly write only the last one.
var (
	cbWriters  = map[string]bool{"rm": true, "rmdir": true, "chmod": true, "chown": true, "chgrp": true, "chattr": true, "truncate": true, "touch": true, "mkdir": true, "tee": true, "patch": true, "shred": true, "unlink": true, "vi": true, "vim": true, "nvim": true, "nano": true, "emacs": true, "ed": true, "mv": true, "ln": true}
	cbDestOnly = map[string]bool{"cp": true, "install": true, "rsync": true, "scp": true}
	// kernel / boot / security toggles (any arguments)
	cbKernelCmd = map[string]bool{"modprobe": true, "insmod": true, "rmmod": true, "depmod": true, "update-grub": true, "grub-install": true, "grub-mkconfig": true,
		"dracut": true, "mkinitcpio": true, "update-initramfs": true, "kexec": true, "setenforce": true, "dockerd": true}
)

// cbSystemCritical returns why a tool call touches the system in a way no
// project task needs ("" = it does not). Read-only commands never count.
func cbSystemCritical(tool string, args map[string]any, cmd string) string {
	switch tool {
	case "write", "edit":
		if p, _ := argStr(args, "path"); p != "" {
			if why := cbSysPath(p); why != "" {
				return "escreve em " + why
			}
		}
		return ""
	case "bash":
	default:
		return ""
	}
	if strings.TrimSpace(cmd) == "" || IsReadOnly(cmd) {
		return ""
	}
	for _, seg := range shellSegments(cmd) {
		if why := cbSegCritical(seg); why != "" {
			return why
		}
	}
	return ""
}

// cbSegCritical checks one simple command.
func cbSegCritical(seg string) string {
	raw := shellFields(seg)
	// redirections first: "> /etc/x", ">>/etc/x", "2>/sys/x"
	for i, t := range raw {
		if j := strings.IndexAny(t, ">"); j >= 0 {
			target := strings.TrimLeft(t[j:], ">&")
			if target == "" && i+1 < len(raw) {
				target = raw[i+1]
			}
			if why := cbSysPath(target); why != "" {
				return "redireciona saída para " + why
			}
		}
	}
	t := stripWrappers(raw)
	for len(t) > 0 && (t[0] == "sudo" || t[0] == "doas" || t[0] == "pkexec") {
		t = stripWrappers(t[1:])
		for len(t) > 0 && strings.HasPrefix(t[0], "-") {
			t = t[1:]
		}
	}
	if len(t) == 0 {
		return ""
	}
	w := filepath.Base(t[0])
	args := t[1:]
	if cbKernelCmd[w] {
		return "mexe no kernel/boot (" + w + ")"
	}
	switch w {
	case "sysctl":
		for _, a := range args {
			if a == "-w" || strings.Contains(a, "=") || a == "-p" || a == "--system" {
				return "altera parâmetro do kernel (sysctl)"
			}
		}
	case "wsl", "wsl.exe":
		for _, a := range args {
			switch strings.ToLower(a) {
			case "--shutdown", "--unregister", "--set-default-version", "--set-version", "--update", "--install", "--import", "--set-default", "--terminate", "-t":
				return "altera a configuração do WSL (wsl " + a + ")"
			}
		}
	case "podman":
		if len(args) >= 2 && (args[0] == "system" && (args[1] == "reset" || args[1] == "migrate" || args[1] == "renumber") || args[0] == "machine" && (args[1] == "rm" || args[1] == "reset" || args[1] == "init" || args[1] == "set")) {
			return "reconfigura o Podman (podman " + args[0] + " " + args[1] + ")"
		}
	case "docker":
		if len(args) >= 2 && args[0] == "context" && (args[1] == "rm" || args[1] == "use" || args[1] == "update") {
			return "reconfigura o Docker (docker context " + args[1] + ")"
		}
	case "systemctl", "service", "rc-service", "sv":
		verb, unit := "", ""
		for _, a := range args {
			if strings.HasPrefix(a, "-") {
				continue
			}
			if verb == "" {
				verb = a
			} else if unit == "" {
				unit = a
			}
		}
		if w == "service" || w == "rc-service" || w == "sv" {
			verb, unit = unit, verb
		}
		u := strings.TrimSuffix(unit, ".service")
		if (u == "docker" || u == "podman" || u == "containerd" || u == "docker.socket" || u == "podman.socket") &&
			(verb == "stop" || verb == "disable" || verb == "mask" || verb == "edit" || verb == "restart" || verb == "daemon-reload" || verb == "down") {
			return "mexe no serviço do motor de contêiner (" + w + " " + verb + " " + unit + ")"
		}
	case "sed", "perl":
		inplace := false
		for _, a := range args {
			if a == "-i" || strings.HasPrefix(a, "-i") || a == "--in-place" || strings.HasPrefix(a, "--in-place=") {
				inplace = true
			}
		}
		if inplace {
			for _, a := range args {
				if why := cbSysPath(a); why != "" {
					return "edita " + why
				}
			}
		}
	case "dd":
		for _, a := range args {
			if strings.HasPrefix(a, "of=") {
				if why := cbSysPath(a[3:]); why != "" {
					return "escreve com dd em " + why
				}
			}
		}
	}
	if cbWriters[w] {
		for _, a := range args {
			if strings.HasPrefix(a, "-") {
				continue
			}
			if why := cbSysPath(a); why != "" {
				return "altera " + why + " (" + w + ")"
			}
		}
	}
	if cbDestOnly[w] {
		for i := len(args) - 1; i >= 0; i-- {
			if strings.HasPrefix(args[i], "-") {
				continue
			}
			if why := cbSysPath(args[i]); why != "" {
				return "copia para " + why + " (" + w + ")"
			}
			break
		}
	}
	return ""
}

// cbContainerGoal: the request is about running a container.
func cbContainerGoal(goal string) bool {
	g := fzFold(goal)
	for _, k := range []string{"docker", "podman", "container", "conteiner", "compose", "kubectl", "imagem docker"} {
		if strings.Contains(g, k) {
			return true
		}
	}
	return false
}

// cbDisproportionate: an action that a container task never needs (removing
// or reinstalling the engine, wiping its storage).
func cbDisproportionate(goal, cmd string) string {
	if !cbContainerGoal(goal) || strings.TrimSpace(cmd) == "" {
		return ""
	}
	for _, seg := range shellSegments(cmd) {
		t := stripWrappers(shellFields(seg))
		for len(t) > 0 && (t[0] == "sudo" || t[0] == "doas") {
			t = stripWrappers(t[1:])
		}
		if len(t) < 2 {
			continue
		}
		w := filepath.Base(t[0])
		rest := strings.Join(t[1:], " ")
		engine := strings.Contains(rest, "docker") || strings.Contains(rest, "podman") || strings.Contains(rest, "containerd") || strings.Contains(rest, "crun") || strings.Contains(rest, "runc")
		switch w {
		case "apt", "apt-get", "dnf", "yum", "zypper", "pacman", "apk", "brew", "snap", "pkg":
			if engine && (strings.Contains(rest, "remove") || strings.Contains(rest, "purge") || strings.Contains(rest, "reinstall") || strings.Contains(rest, "-R") || strings.Contains(rest, "autoremove") || strings.Contains(rest, "uninstall")) {
				return "remover/reinstalar o motor de contêiner (" + w + " " + oneLine(rest, 60) + ")"
			}
		case "rm":
			if strings.Contains(rest, "containers") || strings.Contains(rest, "/var/lib/docker") {
				return "apagar o armazenamento do motor de contêiner"
			}
		}
		if (w == "podman" || w == "docker") && t[1] == "system" && len(t) > 2 && (t[2] == "prune" || t[2] == "reset") {
			return "limpar o motor inteiro (" + w + " system " + t[2] + ")"
		}
	}
	return ""
}

// cbProportionNote explains the block in terms of the user's goal.
func cbProportionNote(goal string) string {
	if !cbContainerGoal(goal) {
		return ""
	}
	return " — para subir ou usar um contêiner não é preciso mexer no sistema, no kernel nem na configuração do motor; a ação é desproporcional ao objetivo"
}

// cbMinimalIntervention is the permit() check (agent.go): "" = allowed.
func cbMinimalIntervention(tool string, args map[string]any, cmd, goal string) string {
	if why := cbSystemCritical(tool, args, cmd); why != "" {
		return "CRÍTICO (menor intervenção): " + why + cbProportionNote(goal)
	}
	if tool == "bash" {
		if why := cbDisproportionate(goal, cmd); why != "" {
			return "CRÍTICO (menor intervenção): " + why + " — desproporcional para uma tarefa de contêiner"
		}
	}
	return ""
}

// ---------- read-only registry commands ----------

// cbReadCmds are the exact argv the brain may run as read-only even where
// the generic bash classifier would ask (untrusted folder, -p without
// confirmation). Exact strings only: no shell metacharacters, no arguments
// beyond these. The generic IsReadOnly classifier is NOT widened: the
// exemption (cbReadCtx) is granted only by the brain executing a registry
// command, never to a command typed through a model tool call.
var cbReadCmds = map[string]bool{
	"git status --short --branch": true,
	"git log --oneline -20":       true,
	"git diff --stat":             true,
	"git diff":                    true,
	"git branch":                  true,
	"git stash list":              true,
	"tailscale status":            true,
	"docker ps":                   true,
	"podman ps":                   true,
	"systemctl is-system-running": true,
	"systemctl list-units --type=service --state=running --no-legend --no-pager --plain": true,
	"df -h":                                  true,
	"free -m":                                true,
	"uptime":                                 true,
	"ss -tln":                                true,
	"ps -eo pid,comm,%mem,%cpu --sort=-%mem": true,
}

// cbReadCmd reports whether cmd is an allowlisted read-only registry
// command: one of cbReadCmds, or "<tool> --version" for a bare tool name.
func cbReadCmd(cmd string) bool {
	if cbReadCmds[cmd] {
		return true
	}
	t, ok := strings.CutSuffix(cmd, " --version")
	if !ok || t == "" || len(t) > 64 {
		return false
	}
	for _, r := range t {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return t[0] != '-' && t[0] != '.'
}

type cbReadKey struct{}

// cbReadCtx marks ctx as carrying a brain registry command that is read-only.
// It is a no-op for a command outside the allowlist.
func cbReadCtx(ctx context.Context, cmd string) context.Context {
	if !cbReadCmd(cmd) {
		return ctx
	}
	return context.WithValue(ctx, cbReadKey{}, cmd)
}

// cbReadGranted: ctx was marked by cbReadCtx for exactly this command.
func cbReadGranted(ctx context.Context, cmd string) bool {
	c, _ := ctx.Value(cbReadKey{}).(string)
	return c != "" && c == cmd && cbReadCmd(cmd)
}
