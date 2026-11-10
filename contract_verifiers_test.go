package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// contract_verifiers_test.go — os dois sem-verificador pagos: o alvo que o
// _fill produz existe de verdade, e o port_owner no-python continua
// honestamente blocked (o verificador declara a prova, não a inventa).
// _fill.by_find com namelen: FillAs do pedido produz namelen=??????? e o
// slot chega ao script — sem isso o take pegava o primeiro .log do glob.
// zzz.log ordena depois de aa-toolongname.log: sem o filtro morria o errado.
func TestBrainFillNamelenChain(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("sem python3")
	}
	root := brFixture(t, "aa-toolongname.log", "x\n", "zzz.log", "x\n")
	env, _ := brTestEnv(t, root, nil, -1)
	in := Intent{Name: "delete_file", Confidence: 0.97, Missing: []string{"target"},
		Slots: map[string]string{"ext": "log", "type": "file"}}
	r := brRunIntent(context.Background(), env,
		"delete the .log file whose name is 7 characters long", in, brResult{})
	if r.Status != tkSucceeded {
		t.Fatalf("%s:\n%s", r.Status, r.Text)
	}
	if _, err := os.Stat(filepath.Join(root, "zzz.log")); !os.IsNotExist(err) {
		t.Errorf("zzz.log (7 chars) devia ter sido apagado:\n%s", r.Text)
	}
	if _, err := os.Stat(filepath.Join(root, "aa-toolongname.log")); err != nil {
		t.Errorf("aa-toolongname.log sumiu — namelen não filtrou:\n%s", r.Text)
	}
}

// port_owner.no_python: read_cmd + report blocked. O verificador cmd_ran
// declara a prova real ("mostrei as portas") sem transformar o blocked
// em verde — a resposta continua honestamente bloqueada.
func TestBrainPortOwnerNoPython(t *testing.T) {
	root := brFixture(t)
	env, rec := brTestEnv(t, root, map[string]brFakeOut{
		"ss -tln": {out: "State Recv-Q Send-Q Local:Port\nLISTEN 0 128 *:8080 *:*\n", exit: 0},
	}, -1)
	bins := env.Profile.PathBins[:0]
	for _, b := range env.Profile.PathBins {
		if b != "python3" {
			bins = append(bins, b)
		}
	}
	env.Profile.PathBins = bins
	r := brRun(context.Background(), env, "quem usa a porta 8080")
	if r.Status != tkBlocked {
		t.Fatalf("%s:\n%s", r.Status, r.Text)
	}
	if !strings.Contains(r.Text, "LISTEN") {
		t.Errorf("as portas lidas deviam constar na resposta:\n%s", r.Text)
	}
	found := false
	for _, c := range rec.cmds {
		found = found || c == "ss -tln"
	}
	if !found {
		t.Errorf("ss -tln não rodou: %v", rec.cmds)
	}
}
