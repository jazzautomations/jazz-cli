package main

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"
)

func nuTestWorld(t *testing.T) (*nuModel, *nuWorld) {
	t.Helper()
	m := nuGet()
	if m.loadErr != nil {
		t.Fatal(m.loadErr)
	}
	return m, nuWorldFromDef(m.data.World)
}

// nuPredict is what the caller acts on: the intent when understood, else none.
func nuPredict(in Intent) string {
	if in.Understood() {
		return in.Name
	}
	return "none"
}

// The dataset is the test: >= 95% of the examples land on their intent.
func TestNLUDatasetAccuracy(t *testing.T) {
	m, w := nuTestWorld(t)
	total, ok := 0, 0
	var bad []string
	for _, it := range m.data.Intents {
		for _, ex := range it.Examples {
			total++
			in := m.Parse(ex, w)
			if nuPredict(in) == it.Name {
				ok++
			} else {
				bad = append(bad, it.Name+" <- "+ex+" => "+in.Name+" "+strings.Join(in.Evidence, " | "))
			}
		}
	}
	acc := float64(ok) / float64(total)
	t.Logf("NLU exemplos: %d/%d = %.1f%%", ok, total, acc*100)
	for _, b := range bad {
		t.Log("  " + b)
	}
	if acc < 0.95 {
		t.Fatalf("acurácia %.1f%% < 95%%", acc*100)
	}
}

// Negatives never land on the intent that lists them.
func TestNLUNegatives(t *testing.T) {
	m, w := nuTestWorld(t)
	for _, it := range m.data.Intents {
		for _, neg := range it.Negatives {
			if in := m.Parse(neg, w); nuPredict(in) == it.Name {
				t.Errorf("%q não deveria ser %s (%v)", neg, it.Name, in.Evidence)
			}
		}
	}
}

// The blind set is measured, never tuned on: the number is reported as is.
func TestNLUBlindSet(t *testing.T) {
	m, w := nuTestWorld(t)
	b, err := os.ReadFile("nlu/blind_test.json")
	if err != nil {
		t.Fatal(err)
	}
	var bs struct {
		Items []struct {
			T      string `json:"t"`
			Intent string `json:"intent"`
		} `json:"items"`
	}
	if err := json.Unmarshal(b, &bs); err != nil {
		t.Fatal(err)
	}
	ok := 0
	var bad []string
	for _, x := range bs.Items {
		in := m.Parse(x.T, w)
		if nuPredict(in) == x.Intent {
			ok++
		} else {
			bad = append(bad, x.Intent+" <- "+x.T+" => "+in.Name)
		}
	}
	acc := float64(ok) / float64(len(bs.Items))
	t.Logf("NLU conjunto cego: %d/%d = %.1f%%", ok, len(bs.Items), acc*100)
	sort.Strings(bad)
	for _, x := range bad {
		t.Log("  " + x)
	}
	if len(bs.Items) != 100 {
		t.Fatalf("o conjunto cego deve ter 100 frases, tem %d", len(bs.Items))
	}
	if acc < 0.70 {
		t.Fatalf("acurácia no conjunto cego %.1f%% caiu abaixo do piso de regressão (70%%)", acc*100)
	}
}

// BenchmarkNLUParse: one request against the dataset world (warm model).
func BenchmarkNLUParse(b *testing.B) {
	m := nuGet()
	w := nuWorldFromDef(m.data.World)
	m.Parse("roda os testes", w) // trains the classifier
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		m.Parse("abre o teste do parsr pra mim", w)
	}
}

// Entity-first routing: a generic status verb is scoped to the tool it names.
func TestNLUToolStatusEntityFirst(t *testing.T) {
	m, w := nuTestWorld(t)
	cases := map[string]string{
		"talscle status": "tool_status", "status do tailscale": "tool_status", "tailscale ta rodando?": "tool_status",
		"docker ta de pé?": "tool_status", "como ta o podman": "tool_status", "is docker running": "tool_status",
		"status": "git_status", "git status": "git_status", "status do git": "git_status",
		"status do nginx": "service_status", "status do labbox": "host_status", "status do ambiente": "env_status",
		"lista os containers": "container_list", "qual a versão do docker": "tool_info",
	}
	for text, want := range cases {
		if in := m.Parse(text, w); nuPredict(in) != want {
			t.Errorf("%q => %s (%v), want %s", text, nuPredict(in), in.Evidence, want)
		}
	}
	if in := m.Parse("talscle status", w); in.Slots["tool"] != "tailscale" {
		t.Errorf("slot tool = %q", in.Slots["tool"])
	}
}
