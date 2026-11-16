package main

// Named-deliverables gate (honest-completion guard).
//
// When the request names output files next to creation verbs ("gere um
// ebook.pdf", "create landing/index.html", "salve o relatório em
// research.md"), "done" is not honest while one of them is missing, empty
// or the wrong format — the d610 demo failure was exactly this: a 2-page
// placeholder declared finished. Everything here is deterministic (stat +
// small read); no model call, no git, one check per turn.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// dvMax: at most this many deliverables are tracked per request.
const dvMax = 12

// dvPathRE: file paths with an artifact or code extension. URLs are
// filtered afterwards (a link is not a deliverable).
var dvPathRE = lazyRx(`(?i)(?:[\w.~+@-]+/)*[\w.+-]+\.(?:pdf|epub|docx|xlsx|pptx|odt|ods|odp|html?|css|md|markdown|txt|csv|tsv|json|xml|ya?ml|toml|ini|conf|svg|png|jpe?g|gif|webp|mp4|webm|mov|mp3|wav|zip|tar|gz|bz2|xz|sqlite3?|db|sql|qmd|ipynb|go|py|js|ts|tsx|jsx|rs|c|h|cpp|java|kt|rb|php|sh|bash|lua|pl|swift|cs|scala|zig|exs?|vue|svelte|dart)\b`)

// dvVerbRE: creation verbs (pt-BR + en) within the same clause as the path
// — distinguishes "crie ebook.pdf" (deliverable) from "leia relatorio.py"
// (input) and from paths mentioned in passing. "faz/faça" stays out on
// purpose: "o que ele faz" would turn a read request into a deliverable.
var dvVerbRE = lazyRx(`(?i)\b(?:cri\w*|ger\w*|produz\w*|escrev\w*|salv\w*|mont\w*|export\w*|rend\w*|compil\w*|build\w*|mak\w*|creat\w*|generat\w*|writ\w*|sav\w*|draft\w*|render\w*|emit\w*|entreg\w*|deliver\w*|publi\w*|baix\w*|download\w*)\b`)

// dvClauseRE splits the request into clauses; the verb must share a clause
// with the path. '.', ':' and ',' are NOT separators: "site/index.html"
// contains '.', and "entregue: ebook.pdf" would lose its verb to ':' —
// splitting on them both produces and drops false positives. The rare
// false positive that survives ("leia foo.py. Depois crie out.pdf") is
// harmless: an existing input file passes the check.
var dvClauseRE = lazyRx(`[\n;,!?()\[\]]`)

// dvPdfPageRE: a readable page object — hand-made and most generated PDFs
// keep /Type /Page uncompressed; compressed object streams just make the
// page check unverifiable (then size alone decides).
var dvPdfPageRE = lazyRx(`(?s)/Type[ \t\r\n]*/Page\b`)

// dvNamed lists the file paths the request explicitly asks to produce.
func dvNamed(req string) []string {
	var out []string
	seen := map[string]bool{}
	clauses := dvClauseRE.Split(req, -1)
	for _, cl := range clauses {
		if !dvVerbRE.MatchString(cl) {
			continue
		}
		for _, loc := range dvPathRE.FindAllStringIndex(cl, -1) {
			p := cl[loc[0]:loc[1]]
			if pre := strings.TrimSpace(cl[:loc[0]]); strings.HasSuffix(pre, "://") || strings.HasSuffix(pre, "@") {
				continue // URL / e-mail, not a path
			}
			p = strings.Trim(p, "`'\"<>")
			if p == "" || seen[p] {
				continue
			}
			seen[p] = true
			out = append(out, p)
			if len(out) >= dvMax {
				return out
			}
		}
	}
	return out
}

// dvCheck describes one deliverable's problem ("" = holds).
func dvCheck(root, p string) string {
	path := p
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	fi, err := os.Stat(path)
	if err != nil {
		return "does not exist"
	}
	if fi.IsDir() {
		return "is a directory, not the file the request named"
	}
	if fi.Size() == 0 {
		return "is empty (0 bytes)"
	}
	// format + size floor
	b := make([]byte, min(fi.Size(), 65536))
	f, err := os.Open(path)
	if err != nil {
		return "cannot be read"
	}
	n, _ := f.Read(b)
	f.Close()
	b = b[:n]
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".pdf":
		if !strings.HasPrefix(string(b), "%PDF-") {
			return "is not a valid PDF (missing %PDF- header)"
		}
		if n < 1024 {
			return fmt.Sprintf("PDF of %d bytes — too small to have real content", n)
		}
		if !dvPdfPageRE.Match(b) && n < 8192 {
			return "PDF with no readable page objects — probably broken"
		}
	case ".html", ".htm":
		low := strings.ToLower(string(b))
		if !strings.Contains(low, "<html") && !strings.Contains(low, "<!doctype") {
			return "has no HTML document (no <html> or <!doctype>)"
		}
		if n < 200 {
			return fmt.Sprintf("HTML of %d bytes — too small for a page", n)
		}
	case ".zip":
		if !strings.HasPrefix(string(b), "PK") {
			return "not a zip file"
		}
	case ".png":
		if len(b) < 8 || string(b[1:4]) != "PNG" {
			return "not a PNG file"
		}
	case ".jpg", ".jpeg":
		if len(b) < 3 || b[0] != 0xFF || b[1] != 0xD8 {
			return "not a JPEG file"
		}
	case ".mp4", ".mov":
		if len(b) < 12 || !strings.Contains(string(b[4:12]), "ftyp") {
			return "not a valid video file (missing ftyp)"
		}
	case ".sqlite", ".sqlite3", ".db":
		if len(b) < 16 || !strings.HasPrefix(string(b), "SQLite format 3") {
			return "not a SQLite database"
		}
	case ".gz":
		if len(b) < 2 || b[0] != 0x1F || b[1] != 0x8B {
			return "not a gzip file"
		}
	default:
		// text/code/other: existence + non-empty is the honest floor —
		// "crie a.txt" holding "oi" is a real file, and judging prose
		// substance deterministically cannot work; that stays with --until.
		if strings.TrimSpace(string(b)) == "" {
			return "only whitespace — an empty deliverable"
		}
	}
	return ""
}

// acDeliverablesGate runs the named-deliverables check once per turn; true
// = a reminder went back to the model (the turn continues).
func (a *Agent) acDeliverablesGate(ctx context.Context, lt *acTurnLoop) bool {
	if a.sub || lt == nil || lt.delivChecked || ctx.Err() != nil {
		return false
	}
	lt.delivChecked = true
	req := strings.TrimSpace(acStripReminders(lt.input))
	if req == "" {
		return false
	}
	var bad []string
	for _, p := range dvNamed(req) {
		if why := dvCheck(a.tools.Root, p); why != "" {
			bad = append(bad, fmt.Sprintf("- %s: %s", p, why))
		}
	}
	if len(bad) == 0 {
		return false
	}
	a.ui.Warn(fmt.Sprintf("entregáveis nomeados pendentes: %d", len(bad)))
	a.append(Message{Role: "user", Content: "<system-reminder>The request named deliverables that do not hold yet:\n" + strings.Join(bad, "\n") + "\nCreate each one for real and verify it (open it, run it, check its size/format). If you cannot deliver one, say so explicitly — never claim it done. " + acGateAnswerNote + "</system-reminder>"})
	return true
}
