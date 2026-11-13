package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pcScripted runs steps (one bash call each), then replies final. The
// tests lower the step floor so a short scripted run can be stored.
func pcScripted(steps []string, final string) func(int, recReq) fakeResp {
	return func(n int, r recReq) fakeResp {
		if n < len(steps) {
			return fakeResp{Calls: []fakeCall{{Name: "bash", Args: fmt.Sprintf(`{"command":%q}`, steps[n])}}}
		}
		return fakeResp{Text: final}
	}
}

func pcRunScripted(t *testing.T, req string, steps []string, final string) []pcPlan {
	t.Helper()
	pl, _ := pcRunScriptedRoot(t, req, steps, final)
	return pl
}

func pcRunScriptedRoot(t *testing.T, req string, steps []string, final string) ([]pcPlan, string) {
	t.Helper()
	t.Setenv("JAZZ_PC_MIN_STEPS", "2")
	h := newHarness(t, pcScripted(steps, final), func(c *Config) { c.MaxSteps = 30; c.Mode = "yolo" })
	scWrite(t, h.root, "a.txt", "SEGREDO=hunter2\n")
	h.ag.Run(context.Background(), req)
	return pcLoad(h.root), h.root
}

// A probe (ls/stat/wc/test) is a verification only after a change, on what
// was changed: the d610 wifi turn ("ls" exit 0, "Não consegui") was stored
// as verified and reinjected for days.
func TestPlanCacheProbeNeedsMutation(t *testing.T) {
	read := []string{"cat a.txt", "wc -l a.txt", "head -1 a.txt"}
	if pl := pcRunScripted(t, "arruma a configuracao da rede wireless", append(read, "ls /etc"), "pronto"); len(pl) != 0 {
		t.Errorf("ls with no change stored a plan: %+v", pl)
	}
	mut := []string{"cp a.txt b.txt", "cp a.txt c.txt", "cp a.txt d.txt", "ls /etc"}
	if pl := pcRunScripted(t, "copia os arquivos de configuracao", mut, "pronto"); len(pl) != 0 {
		t.Errorf("a probe on a path the turn did not change stored a plan: %+v", pl)
	}
	mv := []string{"cp a.txt b.txt", "mv b.txt c.txt", "test -f c.txt"}
	if pl := pcRunScripted(t, "renomeia o arquivo de configuracao", mv, "pronto"); len(pl) != 1 || pl[0].Verify != "test -f c.txt" {
		t.Errorf("mv then test -f on the moved file is a verification: %+v", pl)
	}
}

// A reply that admits failure is never stored, whatever the checks said.
func TestPlanCacheFailureReplyNotStored(t *testing.T) {
	steps := []string{"cp a.txt b.txt", "test -f b.txt"}
	for _, final := range []string{"Não consegui arrumar o wifi: precisa de root.", "Nenhuma alteracao concluida." /* (o servidor falso corta UTF-8 no meio) */, "I couldn't fix it."} {
		if pl := pcRunScripted(t, "arruma a configuracao do wifi", steps, final); len(pl) != 0 {
			t.Errorf("%q: stored %+v", final, pl)
		}
	}
}

// A text command's literal never reaches the plan file.
func TestPlanCacheNoSecretLiteral(t *testing.T) {
	steps := []string{"sed -i 's/^SEGREDO.*/x/' a.txt", "grep SEGREDO a.txt > b.txt", "echo SEGREDO=hunter2 > c.txt", "test -f c.txt"}
	pl, root := pcRunScriptedRoot(t, "limpa a chave do arquivo de configuracao", steps, "pronto")
	if len(pl) != 1 {
		t.Fatalf("plans: %+v", pl)
	}
	b, _ := os.ReadFile(pcPath(root))
	all := fmt.Sprintf("%+v %s", pl[0], b)
	for _, bad := range []string{"SEGREDO", "hunter2"} {
		if strings.Contains(all, bad) {
			t.Errorf("plan carries %q:\n%s", bad, all)
		}
	}
	if !strings.Contains(strings.Join(pl[0].Cmds, "\n"), "sed «literal omitido» a.txt") {
		t.Errorf("verb and path should stay: %v", pl[0].Cmds)
	}
	if dsEligible(pcPlan{Wins: 9, Cmds: pl[0].Cmds}) == "" {
		t.Error("a command with an omitted literal cannot be distilled")
	}
}

func TestPcSafeCmd(t *testing.T) {
	for in, want := range map[string]string{
		"sed 's/^SEGREDO.*/x/' f":                  "sed «literal omitido» f",
		"sudo sed -i \"s/psk=.*/psk=abc/\" /etc/x": "sudo sed «literal omitido» /etc/x",
		"grep -r SENHA src/":                       "grep «literal omitido» src/",
		"echo 'token=abc' >> .env":                 "echo «literal omitido» >> .env",
		"printf '%s' segredo | sudo tee /etc/y":    "printf «literal omitido» | …",
		"go test ./...":                            "go test ./...",
		"ls -la /etc":                              "ls -la /etc",
	} {
		if got := pcSafeCmd(in); got != want {
			t.Errorf("pcSafeCmd(%q) = %q, want %q", in, got, want)
		}
	}
}

// "bora" is not a task: the plan stores the last substantive request.
func TestPlanCacheFollowUpTask(t *testing.T) {
	steps := []string{"cp a.txt b.txt", "test -f b.txt"}
	n := 0
	t.Setenv("JAZZ_PC_MIN_STEPS", "2")
	h := newHarness(t, func(i int, r recReq) fakeResp {
		n++
		if n == 1 {
			return fakeResp{Text: "posso começar?"}
		}
		return pcScripted(steps, "pronto")(n-2, r)
	}, func(c *Config) { c.MaxSteps = 30; c.Mode = "yolo" })
	scWrite(t, h.root, "a.txt", "x\n")
	h.ag.Run(context.Background(), "corrige o parser do csv")
	h.ag.Run(context.Background(), "bora")
	pl := pcLoad(h.root)
	if len(pl) != 1 || pl[0].Task != "corrige o parser do csv" {
		t.Fatalf("follow-up task: %+v", pl)
	}
}

// An injected plan the turn did not follow earns no win; a correction in
// the next message counts a fail; a curse word alone does not.
func TestPlanCacheWinsAndCorrections(t *testing.T) {
	h := newHarness(t, seq(fakeResp{Text: "ok"}), nil)
	root := h.root
	pcSave(root, []pcPlan{
		{ID: "a", Keys: []string{"parser", "csv"}, Files: []string{"parser.go"}, Cmds: []string{"go test ./parser"}, Wins: 1},
		{ID: "b", Keys: []string{"wifi", "rede"}, Cmds: []string{"nmcli dev wifi"}, Wins: 1},
	})
	got := pcFollowed(root, []string{"a", "b"}, []string{"parser.go", "go test ./parser"})
	if strings.Join(got, ",") != "a" {
		t.Errorf("followed: %v", got)
	}
	h.ag.pcPrev = pcPrevTurn{ids: []string{"b"}, req: "arruma o wifi"}
	h.ag.pcCorrection("porra, ficou bom")
	if pl := pcLoad(root); pl[1].Fails != 0 {
		t.Errorf("a curse word is not a correction: %+v", pl[1])
	}
	h.ag.pcPrev = pcPrevTurn{ids: []string{"b"}, req: "arruma o wifi"}
	h.ag.pcCorrection("nao, to falando da sessao do claude porra")
	if pl := pcLoad(root); pl[1].Fails != 1 {
		t.Errorf("an explicit negation is a correction: %+v", pl[1])
	}
	if h.ag.lrHint(pcLoad(root), nil, "oi") != "" {
		t.Error("no lesson on a greeting")
	}
}

// Plans written before 0.14 are migrated once on load, with a backup.
func TestPlanCacheMigration(t *testing.T) {
	t.Setenv("JAZZ_HOME", t.TempDir())
	root := t.TempDir()
	old := []pcPlan{
		{ID: "dc2e261ebfea", Task: "arruma o wifi do notebook", Keys: []string{"wifi", "notebook"}, Verify: "go test ./..."},
		{ID: "p-probe", Task: "configura a rede wireless", Keys: []string{"rede", "wireless"}, Cmds: []string{"iwconfig"}, Verify: "ls /etc/network"},
		{ID: "p-curse", Task: "porra arruma ae essa merda", Keys: []string{"porra", "arruma", "merda"}, Verify: "go test ./..."},
		{ID: "p-good", Task: "corrige o parser do csv", Keys: []string{"corrige", "parser", "csv"}, Cmds: []string{"sed -i 's/SEGREDO/x/' a.go", "go test ./..."}, Verify: "go test ./..."},
		{ID: "p-mv", Task: "renomeia o arquivo de log", Keys: []string{"renomeia", "arquivo", "log"}, Cmds: []string{"mv a.log b.log"}, Verify: "test -f b.log"},
	}
	var b strings.Builder
	for _, p := range old {
		j, _ := json.Marshal(p) // V = 0: written by 0.13
		b.Write(append(j, '\n'))
	}
	os.MkdirAll(filepath.Dir(pcPath(root)), 0o700)
	if err := os.WriteFile(pcPath(root), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	pl := pcLoad(root)
	var ids []string
	for _, p := range pl {
		ids = append(ids, p.ID)
	}
	if strings.Join(ids, ",") != "p-good,p-mv" {
		t.Fatalf("after migration: %v", ids)
	}
	if strings.Contains(strings.Join(pl[0].Cmds, " "), "SEGREDO") {
		t.Errorf("literal kept: %v", pl[0].Cmds)
	}
	if _, err := os.Stat(pcPath(root) + ".pre-0.14.bak"); err != nil {
		t.Errorf("no backup: %v", err)
	}
	if again := pcLoad(root); len(again) != 2 {
		t.Errorf("migration is one-shot: %d", len(again))
	}
}
