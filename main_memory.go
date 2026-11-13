package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// /remember and /memory: Claude Code's "#" memory, on the AGENTS.md files
// jazz already loads (prompt.go loadInstructionsDirs).

// mmProjectFile: the project's instruction file (an existing AGENTS.md or
// CLAUDE.md at the root, else a new AGENTS.md there).
func mmProjectFile(root string) string {
	for _, n := range []string{"AGENTS.md", "CLAUDE.md"} {
		p := filepath.Join(root, n)
		if fi, err := os.Lstat(p); err == nil && fi.Mode().IsRegular() {
			return p
		}
	}
	return filepath.Join(root, "AGENTS.md")
}

func mmGlobalFile() string { return filepath.Join(configDir(), "AGENTS.md") }

// mmAppend adds "- text" to path under a "## Notas" section (created once).
func mmAppend(path, text string) error {
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	s := string(b)
	if s != "" && !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	if !strings.Contains(s, "\n## Notas\n") && !strings.HasPrefix(s, "## Notas\n") {
		if s != "" {
			s += "\n"
		}
		s += "## Notas\n"
	}
	s += "- " + strings.TrimSpace(strings.ReplaceAll(text, "\n", " ")) + "\n"
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(s), 0o644)
}

func (a *App) rememberCmd(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		a.ui.Notice("uso: /remember <regra ou fato> — guarda no AGENTS.md (do projeto ou global) e vale já nesta conversa")
		return
	}
	if redactSecrets(text) != text || nlCredScrub(text) != text {
		a.ui.Warn("isso parece ter um segredo (chave, token ou senha) — não guardo em AGENTS.md")
		return
	}
	proj, glob := mmProjectFile(a.root), mmGlobalFile()
	home, _ := os.UserHomeDir()
	items := []mnItem{{Label: "Projeto", Hint: a.rel(proj), Key: 'p'}, {Label: "Global (todas as pastas)", Hint: glob, Key: 'g'}, {Label: "Cancelar", Key: 'n'}}
	def := 0
	if filepath.Clean(a.root) == filepath.Clean(home) || a.root == "/" {
		def = 1 // no project here
	}
	var path string
	switch a.ui.Choose("Guardar onde?", items, def) {
	case 0:
		path = proj
	case 1:
		path = glob
	default:
		return
	}
	if err := mmAppend(path, text); err != nil {
		a.ui.Error("não consegui gravar: " + err.Error())
		return
	}
	// the system prompt is built once per session: tell the model now
	a.agent.Note("<system-reminder>The user added this standing instruction (saved to " + filepath.Base(path) + "): " + text + "</system-reminder>")
	a.ui.Notice("guardado em " + path)
}

func (a *App) memoryCmd() {
	for _, f := range []struct{ label, path string }{{"global", mmGlobalFile()}, {"projeto", mmProjectFile(a.root)}} {
		fi, err := os.Stat(f.path)
		if err != nil {
			a.ui.Notice(fmt.Sprintf("%s: %s (não existe)", f.label, f.path))
			continue
		}
		a.ui.Notice(fmt.Sprintf("%s: %s (%d bytes) — edite à vontade; /remember <texto> acrescenta", f.label, f.path, fi.Size()))
	}
}

func (a *App) rel(p string) string {
	if r, err := filepath.Rel(a.root, p); err == nil && !strings.HasPrefix(r, "..") {
		return r
	}
	return p
}

func (a *App) btwCmd(q string) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := a.ui.WatchTurn(cancel, nil)
	a.ui.Thinking("pergunta paralela")
	ans, err := a.agent.Side(ctx, q)
	a.ui.StopThinking()
	stop()
	if err != nil {
		a.ui.Warn(friendlyErr(err))
		return
	}
	a.ui.print(sanitizeTerm(redactSecrets(ans)) + "\n")
}
