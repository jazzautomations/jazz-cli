//go:build !windows

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// named deliverables (deliverables.go): a request that names output files
// may not end "done" while one is missing, empty or the wrong format —
// the d610 demo failure: a 2-page placeholder PDF declared finished.
// ---------------------------------------------------------------------------

func dvPDFBody(size int) string {
	var b strings.Builder
	b.WriteString("%PDF-1.4\n1 0 obj\n<< /Type /Page >>\nendobj\n")
	for b.Len() < size {
		b.WriteString("% filler line for a real page of content ................\n")
	}
	b.WriteString("%%EOF\n")
	return b.String()
}

func TestDVNamed(t *testing.T) {
	for _, tc := range []struct {
		req  string
		want []string
	}{
		{"crie o arquivo ebook.pdf com um guia completo", []string{"ebook.pdf"}},
		{"gere a landing em site/index.html e depois o ebook.pdf", []string{"site/index.html", "ebook.pdf"}},
		{"salve a pesquisa em research.md e crie out/app.py", []string{"research.md", "out/app.py"}},
		{"leia o arquivo relatorio.py e me diga o que faz", nil},
		{"baixe o anexo https://example.com/spec.pdf para mim", nil},
		{"o relatório está em out/rel.pdf, analise", nil},
		{"make a demo.md", []string{"demo.md"}},
		{"", nil},
	} {
		got := dvNamed(tc.req)
		if len(got) != len(tc.want) {
			t.Fatalf("%q: got %v, want %v", tc.req, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("%q: got[%d]=%q, want %q", tc.req, i, got[i], tc.want[i])
			}
		}
	}
}

func TestDVCheck(t *testing.T) {
	root := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("ok.pdf", dvPDFBody(4096))
	write("tiny.pdf", dvPDFBody(600))
	write("fake.pdf", "not a pdf at all, just text with a .pdf name and enough bytes to pass the size floor"+strings.Repeat("x", 2000))
	write("page.html", "<!doctype html><html><body>"+strings.Repeat("conteúdo real da página ", 20)+"</body></html>")
	write("stub.md", "x")
	write("real.md", "# pesquisa\n\n"+strings.Repeat("texto substantivo de pesquisa com links. ", 10))

	for _, tc := range []struct {
		path, wantPrefix string
	}{
		{"missing.pdf", "does not exist"},
		{"tiny.pdf", "too small"},
		{"fake.pdf", "not a valid PDF"},
		{"page.html", ""},
		{"stub.md", ""},
		{"real.md", ""},
		{"ok.pdf", ""},
	} {
		got := dvCheck(root, tc.path)
		if tc.wantPrefix == "" && got != "" {
			t.Errorf("%s: got %q, want ok", tc.path, got)
		}
		if tc.wantPrefix != "" && !strings.Contains(got, tc.wantPrefix) {
			t.Errorf("%s: got %q, want it to mention %q", tc.path, got, tc.wantPrefix)
		}
	}
}

func TestAgentDeliverablesMissingThenMade(t *testing.T) {
	h := newHarness(t, seq(
		fakeResp{Text: "pronto, o ebook está feito"}, // lies: no file yet
		fakeResp{Calls: []fakeCall{{ID: "w1", Name: "write", Args: fmt.Sprintf(`{"path":"ebook.pdf","content":%q}`, dvPDFBody(4096))}}},
		fakeResp{Text: "agora sim, ebook.pdf criado e verificado"},
	), nil)
	stop, err := h.ag.Run(context.Background(), "gere um e-book completo em ebook.pdf sobre dating burnout")
	if err != nil || stop != StopDone {
		t.Fatalf("stop=%s err=%v", stop, err)
	}
	reqs := h.fs.requests()
	if len(reqs) != 3 {
		t.Fatalf("requests = %d, want 3 (done blocked once)", len(reqs))
	}
	if !reqs[1].contains("user", "ebook.pdf") || !reqs[1].contains("user", "does not exist") {
		t.Errorf("deliverables reminder missing: %v", reqs[1].last())
	}
	if !anyContains(h.ui.warns, "entregáveis nomeados pendentes") {
		t.Errorf("user-facing warning missing: %v", h.ui.warns)
	}
}

func TestAgentDeliverablesMadeFirstPassClean(t *testing.T) {
	h := newHarness(t, seq(
		fakeResp{Calls: []fakeCall{{ID: "w1", Name: "write", Args: fmt.Sprintf(`{"path":"ebook.pdf","content":%q}`, dvPDFBody(4096))}}},
		fakeResp{Text: "feito"},
	), nil)
	stop, err := h.ag.Run(context.Background(), "gere um e-book completo em ebook.pdf")
	if err != nil || stop != StopDone {
		t.Fatalf("stop=%s err=%v", stop, err)
	}
	if n := len(h.fs.requests()); n != 2 {
		t.Fatalf("requests = %d, want 2 (no extra round)", n)
	}
}

func TestAgentDeliverablesInputFileNotChecked(t *testing.T) {
	h := newHarness(t, seq(
		fakeResp{Text: "é um script que soma vendas"},
	), nil)
	stop, err := h.ag.Run(context.Background(), "leia o arquivo relatorio.py e me diga o que ele faz")
	if err != nil || stop != StopDone {
		t.Fatalf("stop=%s err=%v", stop, err)
	}
	if n := len(h.fs.requests()); n != 1 {
		t.Fatalf("requests = %d, want 1 (input files are not deliverables)", n)
	}
}
