package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// nuFreshModel compiles the embedded grammar from scratch (nuGet is the
// process singleton; these tests must not mutate it).
func nuFreshModel(t *testing.T) *nuModel {
	t.Helper()
	lx, _ := nuFS.ReadFile("nlu/lexicon.json")
	ds, _ := nuFS.ReadFile("nlu/intents.json")
	m := nuCompile(lx, ds)
	if m.loadErr != nil {
		t.Fatal(m.loadErr)
	}
	return m
}

func nuExamplesOf(m *nuModel, name string) []string {
	return m.data.Intents[m.byName[name]].Examples
}

// Only existing intents, deduplicated by the text the classifier sees, never
// a negative of the same intent nor another intent's example, appended
// after the embedded ones.
func TestOverlayMergeDedup(t *testing.T) {
	m := nuFreshModel(t)
	first := nuExamplesOf(m, "large_funcs")[0]
	n0 := len(nuExamplesOf(m, "large_funcs"))
	neg := m.data.Intents[m.byName["open_file"]].Negatives[0]
	other := nuExamplesOf(m, "dup_code")[0]
	ov := &nuOverlay{Version: 1, Examples: map[string][]string{
		"large_funcs": {
			"lista as funções mais compridas",
			"  Lista as FUNÇÕES mais compridas  ", // same canon
			first,                                 // already embedded
			other,                                 // another intent's example
			"",
			strings.Repeat("funções ", 40), // > 200 chars
			"linha um\nlinha dois",
			"me diz as funções mais compridas",
		},
		"open_file":    {neg}, // a negative of the same intent: poisoned
		"nao_existe":   {"qualquer coisa"},
		"git_cochange": {"arquivos que sempre mudam juntos"},
	}}
	added, skipped := nuApplyOverlay(m, ov)
	if added != 3 {
		t.Errorf("added=%d, esperado 3 (skipped %v)", added, skipped)
	}
	want := map[string]int{"duplicate": 2, "conflict": 1, "invalid": 3, "negative": 1, "intent": 1}
	for k, v := range want {
		if skipped[k] != v {
			t.Errorf("skipped[%s]=%d, esperado %d (%v)", k, skipped[k], v, skipped)
		}
	}
	ex := nuExamplesOf(m, "large_funcs")
	if ex[0] != first || len(ex) != n0+2 || ex[n0] != "lista as funções mais compridas" {
		t.Errorf("overlay vai depois do embarcado (Examples[0] é a frase-modelo): %q", ex[n0:])
	}
	// the merged model learns from it
	w := nuWorldFromDef(m.data.World)
	if in := m.Parse("lista as funções mais compridas", w); !(fgActs(in) && in.Name == "large_funcs") {
		t.Errorf("exemplo do overlay não treinou o classificador: %s %.2f", in.Name, in.Confidence)
	}
}

// The caps hold: 200 per intent, 2000 overall, in sorted intent order (the
// result never depends on map iteration).
func TestOverlayCap(t *testing.T) {
	m := nuFreshModel(t)
	var many []string
	for i := 0; i < nuOverlayPerIntent+50; i++ {
		many = append(many, fmt.Sprintf("funções compridas xq%dz", i))
	}
	added, skipped := nuApplyOverlay(m, &nuOverlay{Version: 1, Examples: map[string][]string{"large_funcs": many}})
	if added != nuOverlayPerIntent || skipped["cap"] != 50 {
		t.Errorf("teto por intenção: added=%d skipped=%v", added, skipped)
	}
	m = nuFreshModel(t)
	ov := &nuOverlay{Version: 1, Examples: map[string][]string{}}
	k := 0
	for _, it := range m.data.Intents[:12] {
		for i := 0; i < nuOverlayPerIntent; i++ {
			ov.Examples[it.Name] = append(ov.Examples[it.Name], fmt.Sprintf("pedido zz%dq", k))
			k++
		}
	}
	added, skipped = nuApplyOverlay(m, ov)
	if added != nuOverlayCap || skipped["cap"] != 12*nuOverlayPerIntent-nuOverlayCap {
		t.Errorf("teto global: added=%d skipped=%v", added, skipped)
	}
}

// The file: missing is normal, anything invalid is ignored with an error
// (the embedded grammar runs as shipped).
func TestOverlayReadFile(t *testing.T) {
	dir := t.TempDir()
	if ov, err := nuReadOverlay(filepath.Join(dir, "nada.json")); ov != nil || err != nil {
		t.Errorf("arquivo ausente = (nil, nil): %v %v", ov, err)
	}
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	for name, body := range map[string]string{
		"lixo.json":   "{não é json",
		"v2.json":     `{"version":2,"examples":{}}`,
		"grande.json": `{"version":1,"examples":{"x":["` + strings.Repeat("a", nuOverlayMaxBytes) + `"]}}`,
	} {
		if ov, err := nuReadOverlay(write(name, body)); ov != nil || err == nil {
			t.Errorf("%s deve ser ignorado com erro: %v %v", name, ov, err)
		}
	}
	ov, err := nuReadOverlay(write("ok.json", `{"version":1,"examples":{"large_funcs":["lista as funções mais compridas"]}}`))
	if err != nil || ov == nil || len(ov.Examples["large_funcs"]) != 1 {
		t.Fatalf("overlay válido: %v %+v", err, ov)
	}
}

// Off switches: a test binary, JAZZ_FORGE=off, --fresh and the eval; and the
// path lives under JAZZ_HOME (tests never read the owner's data dir).
func TestOverlayOffSwitchesAndPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("JAZZ_HOME", home)
	if !strings.HasPrefix(nuOverlayPath(), home) || filepath.Base(nuOverlayPath()) != "intents.overlay.json" {
		t.Errorf("caminho do overlay fora do JAZZ_HOME: %s", nuOverlayPath())
	}
	if nuForgeOn() {
		t.Fatal("binário de teste nunca carrega o overlay sem opt-in")
	}
	nuOverlayTestOn.Store(true)
	defer nuOverlayTestOn.Store(false)
	if !nuForgeOn() {
		t.Fatal("opt-in do teste deve ligar")
	}
	t.Setenv("JAZZ_FORGE", "off")
	if nuForgeOn() {
		t.Error("JAZZ_FORGE=off deve desligar")
	}
	t.Setenv("JAZZ_FORGE", "")
	pfFreshMode.Store(true)
	if nuForgeOn() {
		t.Error("--fresh deve desligar")
	}
	pfFreshMode.Store(false)
	nuOverlayOff.Store(true)
	if nuForgeOn() {
		t.Error("o eval deve desligar")
	}
	nuOverlayOff.Store(false)

	// nuLoadOverlay reads the data-dir file; an invalid one changes nothing
	if err := os.MkdirAll(filepath.Dir(nuOverlayPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(nuOverlay{Version: 1, Examples: map[string][]string{"large_funcs": {"lista as funções mais compridas"}}})
	os.WriteFile(nuOverlayPath(), b, 0o600)
	m := nuFreshModel(t)
	n0 := len(nuExamplesOf(m, "large_funcs"))
	nuLoadOverlay(m)
	if len(nuExamplesOf(m, "large_funcs")) != n0+1 {
		t.Error("nuLoadOverlay não aplicou o arquivo do data dir")
	}
	os.WriteFile(nuOverlayPath(), []byte("{quebrado"), 0o600)
	m = nuFreshModel(t)
	nuLoadOverlay(m)
	if len(nuExamplesOf(m, "large_funcs")) != n0 {
		t.Error("overlay inválido deve ser ignorado")
	}
}
