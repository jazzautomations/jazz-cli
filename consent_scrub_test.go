package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A senha sudo digitada na conversa fica SÓ na memória da sessão: não pode
// aparecer no que vai pro provedor, no histórico de mensagens, nem em arquivo
// nenhum do diretório de dados (sessão, eventos, audit, sinais) ou do projeto.
// Regressão do FINDINGS S1 (medido em 2026-11-09: vazava nos três).
func TestNLCredentialNeverLeavesMemory(t *testing.T) {
	const pw = "Xk9-exemplo!77"
	for _, in := range []string{
		"arruma o wifi. a senha do sudo é " + pw + " pode ir",
		"senha do root: " + pw,
		"instala o pacote\nroot ana " + pw,
	} {
		var sent []string
		h := newHarness(t, func(i int, r recReq) fakeResp {
			b, _ := json.Marshal(r.Messages)
			sent = append(sent, string(b))
			raw, _ := json.Marshal(r.Raw)
			sent = append(sent, string(raw))
			return fakeResp{Text: "ok"}
		}, nil)
		home := os.Getenv("JAZZ_HOME")
		if err := h.run(t, in); err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if c := h.ag.nlCredFor(); c == nil || c.pass != pw {
			t.Fatalf("%q: a credencial tem que continuar capturada na memória: %+v", in, c)
		}
		if len(sent) == 0 {
			t.Fatalf("%q: o provedor falso não recebeu nada", in)
		}
		for _, s := range sent {
			if strings.Contains(s, pw) {
				t.Errorf("%q: a senha foi enviada ao provedor", in)
			}
		}
		if !strings.Contains(strings.Join(sent, "\n"), "credencial-recebida") { // json.Marshal escapa o "<"
			t.Errorf("%q: o modelo tem que ver o marcador %s no lugar da senha", in, nlCredMark)
		}
		for _, m := range h.ag.Msgs {
			if strings.Contains(m.Content, pw) {
				t.Errorf("%q: a senha ficou no histórico de mensagens (%s)", in, m.Role)
			}
		}
		for _, dir := range []string{home, h.root} {
			filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return nil
				}
				if b, e := os.ReadFile(p); e == nil && strings.Contains(string(b), pw) {
					t.Errorf("%q: a senha foi gravada em %s", in, strings.TrimPrefix(p, dir))
				}
				return nil
			})
		}
	}
}

// Mensagem entregue no meio do turno (steer) e item da fila passam pelo mesmo filtro.
func TestNLCredentialScrubbedInSteerAndQueue(t *testing.T) {
	const pw = "Zq7wLm42pT"
	h := newHarness(t, nil, nil)
	if got := h.ag.nlScanConsent("a senha do sudo é " + pw); strings.Contains(got, pw) || !strings.Contains(got, nlCredMark) {
		t.Errorf("nlScanConsent tem que devolver o texto sem a senha: %q", got)
	}
	if c := h.ag.nlCredFor(); c == nil || c.pass != pw {
		t.Fatalf("credencial não capturada: %+v", c)
	}
	// escanear de novo o texto já limpo não troca a credencial pelo marcador
	h.ag.nlScanConsent(h.ag.nlScanConsent("a senha do sudo é " + pw))
	if c := h.ag.nlCredFor(); c == nil || c.pass != pw {
		t.Errorf("o marcador não pode virar credencial: %+v", c)
	}
	if p := qPreview("root ana " + pw); strings.Contains(p, pw) {
		t.Errorf("prévia da fila com senha: %q", p)
	}
}

func TestNLCredScrub(t *testing.T) {
	const pw = "s3nha-exemplo"
	for in, want := range map[string]string{
		"root ana " + pw:                        "root ana " + nlCredMark,
		"a senha do sudo é " + pw + ", valeu":   "a senha do sudo é " + nlCredMark + ", valeu",
		"sudo senha " + pw:                      "sudo senha " + nlCredMark,
		"password: " + pw:                       "password: " + nlCredMark,
		"a senha é forte demais":                "a senha é forte demais",
		"qual a senha do wifi?":                 "qual a senha do wifi?",
		"roda os testes e me diz o que quebrou": "roda os testes e me diz o que quebrou",
	} {
		if got := nlCredScrub(in); got != want {
			t.Errorf("nlCredScrub(%q) = %q, quero %q", in, got, want)
		}
	}
	// "password: X" dentro de uma tarefa longa sem sudo/root é dado da tarefa:
	// o modelo precisa ver o valor e nada vira credencial.
	task := "abre o config.yaml do projeto e troca a linha password: hunter2abc pelo valor que está no arquivo .env.example"
	if got := nlCredScrub(task); got != task {
		t.Errorf("tarefa comum foi tratada como credencial: %q", got)
	}
	h := newHarness(t, nil, nil)
	h.ag.nlScanConsent(task)
	if h.ag.nl.cred != nil {
		t.Errorf("tarefa comum registrou credencial sudo: %+v", h.ag.nl.cred)
	}
}

// O histórico do editor vai pra disco: a senha não entra nele.
func TestEditorHistoryScrubsCredential(t *testing.T) {
	const pw = "Hb3kkP90vv"
	e := &Editor{}
	e.remember("root ana " + pw)
	if len(e.history) != 1 || strings.Contains(e.history[0], pw) {
		t.Errorf("histórico com senha: %q", e.history)
	}
}
