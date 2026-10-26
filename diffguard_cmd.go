package main

// diffguard_cmd.go: `jazz scan [--since ref] [--rules]`, the diff-review rule
// engine on demand (diffguard.go). Without --since it reviews the working
// tree against HEAD (untracked files count as new); with --since ref, against
// that commit. --rules lists the loaded packs and rules. Exit 0: nothing
// risky; 5: CRITICAL/HIGH findings (the same code a denied run exits with).

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func dgGit(root string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	return out.Bytes(), err
}

// dgGitFiles builds the changed-file list of the working tree against ref.
func dgGitFiles(root, ref string) ([]dgFile, error) {
	if _, err := dgGit(root, "rev-parse", "--show-toplevel"); err != nil {
		return nil, fmt.Errorf("a pasta não é um repositório git")
	}
	if ref != "" {
		if _, err := dgGit(root, "rev-parse", "--verify", "--quiet", ref+"^{commit}"); err != nil {
			return nil, fmt.Errorf("referência desconhecida: %s", ref)
		}
	}
	seen := map[string]bool{}
	var files []dgFile
	add := func(rel string, tracked bool) {
		rel = filepath.ToSlash(rel)
		if seen[rel] || dgSkipPath(rel) {
			return
		}
		seen[rel] = true
		f := dgFile{Path: rel}
		abs := filepath.Join(root, filepath.FromSlash(rel))
		fi, err := os.Lstat(abs)
		switch {
		case err != nil:
			f.Deleted = true
		case !fi.Mode().IsRegular() || fi.Size() > dgMaxFile:
			return
		default:
			b, _ := os.ReadFile(abs)
			if bytes.IndexByte(b[:min(len(b), 8000)], 0) >= 0 {
				return
			}
			f.New = string(b)
		}
		if tracked && ref != "" {
			if old, err := dgGit(root, "show", ref+":./"+rel); err == nil {
				f.Old = string(old)
			}
		}
		if f.Old != f.New {
			files = append(files, f)
		}
	}
	if ref != "" {
		out, err := dgGit(root, "diff", "--name-only", "-z", "--no-renames", "--relative", ref, "--", ".")
		if err != nil {
			return nil, fmt.Errorf("git diff falhou")
		}
		for _, p := range cpSplitZ(out) {
			add(p, true)
		}
	}
	out, _ := dgGit(root, "ls-files", "-z", "--others", "--exclude-standard")
	for _, p := range cpSplitZ(out) {
		add(p, false)
	}
	return files, nil
}

func cmdScan(root string, args []string, w io.Writer) int {
	since, rules := "HEAD", false
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--rules" || a == "-rules":
			rules = true
		case a == "--since" || a == "-since":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "uso: jazz scan [--since ref] [--rules]")
				return 2
			}
			i++
			since = args[i]
		case strings.HasPrefix(a, "--since="):
			since = strings.TrimPrefix(a, "--since=")
		default:
			fmt.Fprintln(os.Stderr, "uso: jazz scan [--since ref] [--rules]")
			return 2
		}
	}
	eng := dgLoadEngine(root)
	if rules {
		dgPrintRules(w, eng)
		return 0
	}
	if _, err := dgGit(root, "rev-parse", "--verify", "--quiet", "HEAD"); err != nil && since == "HEAD" {
		since = "" // no commits yet: everything is new
	}
	files, err := dgGitFiles(root, since)
	if err != nil {
		fmt.Fprintln(os.Stderr, "jazz scan:", err)
		return 2
	}
	allow := newAPStoreFile(root, dgAllowFile, dgAllowKind)
	found, allowed := eng.scanAll(files, allow)
	if bin := dgSemgrepPath(); bin != "" {
		sg, err := dgRunSemgrep(context.Background(), bin, root, files)
		if err != nil {
			fmt.Fprintln(os.Stderr, "semgrep:", err)
		}
		found = append(found, dgDropAllowed(sg, allow)...)
	}
	if len(found) == 0 {
		fmt.Fprintf(w, "nada de risco em %d arquivo(s) alterado(s)", len(files))
		if allowed > 0 {
			fmt.Fprintf(w, " (%d achado(s) já permitidos em .jazz/%s)", allowed, dgAllowFile)
		}
		fmt.Fprintln(w)
		return 0
	}
	risky := 0
	for _, f := range found {
		fmt.Fprintln(w, dgHunkText([]dgFinding{f}))
		if f.rank() >= dgSev["HIGH"] {
			risky++
		}
	}
	fmt.Fprintf(w, "\n%d achado(s) em %d arquivo(s), %d de risco alto ou crítico\n", len(found), len(files), risky)
	if risky > 0 {
		return 5
	}
	return 0
}

func dgPrintRules(w io.Writer, e *dgEngine) {
	fmt.Fprintln(w, "pacotes de regras:")
	for _, p := range e.packs {
		line := fmt.Sprintf("  %-8s %3d regra(s)  %s", p.Origin, p.Rules, p.Name)
		if p.Note != "" {
			line += "  [" + p.Note + "]"
		}
		fmt.Fprintln(w, line)
	}
	fmt.Fprintln(w, "\nregras:")
	for _, r := range e.rules {
		fmt.Fprintf(w, "  %-9s %-22s %s\n", r.Severity, r.ID, r.MessagePT)
	}
	if bin := dgSemgrepPath(); bin != "" {
		fmt.Fprintf(w, "\nsemgrep: %s (p/security-audit e p/secrets nos arquivos alterados)\n", bin)
	} else {
		fmt.Fprintln(w, "\nsemgrep: não encontrado (opcional: pip install semgrep)")
	}
}
