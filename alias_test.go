package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// aliasTestHome isolates the data dir (the alias file lives under it).
func aliasTestHome(t *testing.T) {
	t.Helper()
	t.Setenv("JAZZ_HOME", t.TempDir())
}

// The spoken form: "quando eu falar X é Y" parses X and Y; other sentences
// do not match.
func TestAliasRequestParse(t *testing.T) {
	for s, want := range map[string][2]string{
		"quando eu falar fuça é list_files":              {"fuça", "list_files"},
		"quando eu disser 'fuça os logs' é service_logs": {"fuça os logs", "service_logs"},
		"quando eu pedir sobe aí é service_start":        {"sobe aí", "service_start"},
		"quando eu falar fuça é lista os arquivos":       {"fuça", "lista os arquivos"},
	} {
		ph, in, ok := nuAliasRequest(s)
		if !ok || ph != want[0] || in != want[1] {
			t.Errorf("%q -> %q %q %v", s, ph, in, ok)
		}
	}
	for _, s := range []string{"quando eu falo contigo é bom", "fala sério", "", "quando eu falar"} {
		if _, _, ok := nuAliasRequest(s); ok {
			t.Errorf("%q não é pedido de apelido", s)
		}
	}
}

// add → Parse hits at 0.97 with the owner's evidence; forget removes from
// file and live model. The alias is a label, not an authorization: it can
// point at a mutating intent and the permission gate still guards it.
func TestAliasAddForgetParse(t *testing.T) {
	aliasTestHome(t)
	text := "zzqx fuça" // sem palavra da intenção: sem apelido não lê service_logs
	if _, err := nuAliasAdd(text, "service_logs"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { nuAliasForget(text) })
	in := nuGet().Parse(text, nuWorldFromDef(nuGet().data.World))
	if in.Name != "service_logs" || in.Confidence != 0.97 || !strings.Contains(strings.Join(in.Evidence, " "), "apelido") {
		t.Errorf("apelido resolve na hora: %s %.2f %v", in.Name, in.Confidence, in.Evidence)
	}
	// a forma falada resolve Y como frase: "lista os arquivos" → list_files.
	if _, err := nuAliasAdd("zzqy varre tudo", "lista os arquivos"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { nuAliasForget("zzqy varre tudo") })
	if in := nuGet().Parse("zzqy varre tudo", nuWorldFromDef(nuGet().data.World)); in.Name != "list_files" {
		t.Errorf("Y falado vira a intenção lida: %s", in.Name)
	}
	// intenção mutante: o apelido classifica — a permissão continua guardando.
	if _, err := nuAliasAdd("zzqz limpa tudo", "delete_file"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { nuAliasForget("zzqz limpa tudo") })
	if in := nuGet().Parse("zzqz limpa tudo", nuWorldFromDef(nuGet().data.World)); in.Name != "delete_file" {
		t.Errorf("apelido pra mutante classifica (não autoriza): %s", in.Name)
	}
	// esquecer tira do arquivo e do modelo vivo
	if _, err := nuAliasForget(text); err != nil {
		t.Fatal(err)
	}
	af, _ := nuReadAliases()
	if len(af.Aliases) != 2 {
		t.Errorf("ficaram %d apelidos, esperado 2", len(af.Aliases))
	}
	if in := nuGet().Parse(text, nuWorldFromDef(nuGet().data.World)); in.Name == "service_logs" {
		t.Error("apelido esquecido ainda resolve")
	}
}

// Errors: unknown intent and an unlearnable Y; oversized text; a full list.
func TestAliasAddErrors(t *testing.T) {
	aliasTestHome(t)
	if _, err := nuAliasAdd("zzqx coisa", "nao_existe_essa"); err == nil || !strings.Contains(err.Error(), "não conheço") {
		t.Errorf("intenção desconhecida: %v", err)
	}
	if _, err := nuAliasAdd("zzqx coisa", "xqz blub zorp"); err == nil {
		t.Error("Y que não lê intenção nenhuma tem que dar erro")
	}
	if _, err := nuAliasAdd(strings.Repeat("a", 300), "list_files"); err == nil {
		t.Error("apelido grande demais")
	}
	af := &nuAliasFile{Version: nuAliasVersion, Aliases: map[string]nuAlias{}}
	for i := 0; i < nuAliasCap; i++ {
		c := fmt.Sprintf("zzqx%d", i)
		af.Aliases[c] = nuAlias{Text: c, Intent: "list_files"}
	}
	if err := nuWriteAliases(af); err != nil {
		t.Fatal(err)
	}
	if _, err := nuAliasAdd("zzqx mais um", "list_files"); err == nil || !strings.Contains(err.Error(), "limite") {
		t.Errorf("lista cheia: %v", err)
	}
}

// The file loads into a fresh model; aliases to intents that vanished from
// the grammar are skipped, and the file is never read from the repo.
func TestAliasLoadFreshModel(t *testing.T) {
	aliasTestHome(t)
	m := nuFreshModel(t)
	canon := m.canonOf("zzqx fuça os logs")
	af := &nuAliasFile{Version: nuAliasVersion, Aliases: map[string]nuAlias{
		canon:      {Text: "zzqx fuça os logs", Intent: "service_logs"},
		"fantasma": {Text: "fantasma", Intent: "intent_que_nao_existe"},
		"":         {Text: "vazio", Intent: "list_files"},
		"apaga":    {Text: "apaga", Intent: "none"},
	}}
	if err := nuWriteAliases(af); err != nil {
		t.Fatal(err)
	}
	nuLoadAliases(m)
	if len(m.aliases) != 1 || m.aliases[canon] != "service_logs" {
		t.Fatalf("só o válido carrega: %v", m.aliases)
	}
	if in := m.Parse("zzqx fuça os logs", nuWorldFromDef(m.data.World)); in.Name != "service_logs" || in.Confidence != 0.97 {
		t.Errorf("modelo novo resolve o apelido: %s %.2f", in.Name, in.Confidence)
	}
	// arquivo inválido: o modelo fica como saiu de fábrica, sem erro fatal.
	bad := &nuAliasFile{Version: 99, Aliases: map[string]nuAlias{}}
	b, _ := json.Marshal(bad)
	p := nuAliasPath()
	os.MkdirAll(filepath.Dir(p), 0o700)
	os.WriteFile(p, b, 0o600)
	m2 := nuFreshModel(t)
	nuLoadAliases(m2)
	if len(m2.aliases) != 0 {
		t.Error("arquivo inválido não pode carregar apelido nenhum")
	}
}
