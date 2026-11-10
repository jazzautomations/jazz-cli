//go:build !windows

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// contract_slots_test.go — provas de comportamento das dívidas pagas do
// portão estático (evals/contracts/known.txt): cada slot que a gramática
// extrai chega de verdade ao filtro.
// scPaths extrai o conjunto de paths do payload de um script de busca.
func scPaths(v map[string]any) map[string]bool {
	out := map[string]bool{}
	d := scDataOf(v)
	for _, it := range d["files"].([]any) {
		m, _ := it.(map[string]any)
		out[m["path"].(string)] = true
	}
	return out
}

// ---------- script-level: os filtros existem e filtram ----------

func TestScriptFileFindNamelen(t *testing.T) {
	tb, root := scTestBox(t)
	scWrite(t, root, "nl/abcde", "x\n")    // basename 5
	scWrite(t, root, "nl/longname", "x\n") // basename 8
	// "names are 5 characters long" chega como ?×5 (gramática) ou número
	v, _ := scCall(t, tb, "file_find", map[string]any{"path": "nl", "namelen": "5"})
	if !scOK(v) || !scPaths(v)["nl/abcde"] || scPaths(v)["nl/longname"] {
		t.Fatalf("namelen=5: %v", v)
	}
	v, _ = scCall(t, tb, "file_find", map[string]any{"path": "nl", "namelen": "????????"})
	if !scOK(v) || !scPaths(v)["nl/longname"] || scPaths(v)["nl/abcde"] {
		t.Fatalf("namelen glob: %v", v)
	}
	// inválido é falha honesta, nunca filtro descartado em silêncio
	if v, _ := scCall(t, tb, "file_find", map[string]any{"path": "nl", "namelen": "abc"}); scOK(v) {
		t.Fatalf("namelen inválido passou: %v", v)
	}
}

func TestScriptFileFindNotEmptyNotOrphan(t *testing.T) {
	tb, root := scTestBox(t)
	scWrite(t, root, "ne/full.txt", "x\n")
	scWrite(t, root, "ne/void.txt", "")
	v, _ := scCall(t, tb, "file_find", map[string]any{"path": "ne", "type": "file", "not_empty": "true"})
	if !scOK(v) || !scPaths(v)["ne/full.txt"] || scPaths(v)["ne/void.txt"] {
		t.Fatalf("not_empty: %v", v)
	}
	scWrite(t, root, "no/real.txt", "x\n")
	if err := os.Symlink("real.txt", filepath.Join(root, "no/linkbom")); err != nil {
		t.Skipf("sem symlink: %v", err)
	}
	if err := os.Symlink("gone.txt", filepath.Join(root, "no/linkmorto")); err != nil {
		t.Skipf("sem symlink: %v", err)
	}
	v, _ = scCall(t, tb, "file_find", map[string]any{"path": "no", "type": "link", "not_orphan": "true"})
	if !scOK(v) || !scPaths(v)["no/linkbom"] || scPaths(v)["no/linkmorto"] {
		t.Fatalf("not_orphan: %v", v)
	}
}

func TestScriptFileFindTimeRef(t *testing.T) {
	tb, root := scTestBox(t)
	scWrite(t, root, "tr/ref.txt", "x\n")
	scWrite(t, root, "tr/old.txt", "x\n")
	scWrite(t, root, "tr/new.txt", "x\n")
	old := time.Now().Add(-48 * time.Hour)
	os.Chtimes(filepath.Join(root, "tr/old.txt"), old, old)
	ref := time.Now().Add(-time.Hour)
	os.Chtimes(filepath.Join(root, "tr/ref.txt"), ref, ref)
	v, _ := scCall(t, tb, "file_find", map[string]any{"path": "tr", "type": "file", "newer_file": "ref.txt"})
	if !scOK(v) || !scPaths(v)["tr/new.txt"] || scPaths(v)["tr/old.txt"] || scPaths(v)["tr/ref.txt"] {
		t.Fatalf("newer_file: %v", v)
	}
	v, _ = scCall(t, tb, "file_find", map[string]any{"path": "tr", "type": "file", "older_file": "ref.txt"})
	if !scOK(v) || !scPaths(v)["tr/old.txt"] || scPaths(v)["tr/new.txt"] || scPaths(v)["tr/ref.txt"] {
		t.Fatalf("older_file: %v", v)
	}
}

func TestScriptPermFindWritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignora os bits de permissão — os.access sempre diz sim")
	}
	tb, root := scTestBox(t)
	scWrite(t, root, "pw/open.txt", "x\n")
	locked := scWrite(t, root, "pw/locked.txt", "x\n")
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Skipf("sem chmod: %v", err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o644) })
	v, _ := scCall(t, tb, "perm_find", map[string]any{"path": "pw", "type": "file", "writable": "true"})
	if !scOK(v) || !scPaths(v)["pw/open.txt"] || scPaths(v)["pw/locked.txt"] {
		t.Fatalf("writable: %v", v)
	}
	v, _ = scCall(t, tb, "perm_find", map[string]any{"path": "pw", "type": "file", "not_writable": "true"})
	if !scOK(v) || !scPaths(v)["pw/locked.txt"] || scPaths(v)["pw/open.txt"] {
		t.Fatalf("not_writable: %v", v)
	}
}

// ---------- e2e: gramática → método → script, o operando não se perde ----------

func TestBrainFileFindNotEmpty(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("sem python3")
	}
	root := brFixture(t, "ne/full.txt", "x\n", "ne/void.txt", "")
	env, _ := brTestEnv(t, root, nil, -1)
	r := brRun(context.Background(), env, "procura arquivos não vazios")
	if r.Status != tkSucceeded {
		t.Fatalf("%s:\n%s", r.Status, r.Text)
	}
	if !strings.Contains(r.Text, "full.txt") {
		t.Errorf("full.txt devia constar:\n%s", r.Text)
	}
	if strings.Contains(r.Text, "void.txt") {
		t.Errorf("void.txt ficou na resposta — not_empty ignorado:\n%s", r.Text)
	}
}

func TestBrainFileFindNotOrphan(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("sem python3")
	}
	root := brFixture(t, "no/real.txt", "x\n")
	if err := os.Symlink("real.txt", filepath.Join(root, "no/linkbom")); err != nil {
		t.Skipf("sem symlink: %v", err)
	}
	if err := os.Symlink("gone.txt", filepath.Join(root, "no/linkmorto")); err != nil {
		t.Skipf("sem symlink: %v", err)
	}
	env, _ := brTestEnv(t, root, nil, -1)
	r := brRun(context.Background(), env, "find all symbolic links which are not broken")
	if r.Status != tkSucceeded {
		t.Fatalf("%s:\n%s", r.Status, r.Text)
	}
	if !strings.Contains(r.Text, "linkbom") {
		t.Errorf("linkbom devia constar:\n%s", r.Text)
	}
	if strings.Contains(r.Text, "linkmorto") {
		t.Errorf("linkmorto ficou na resposta — not_orphan ignorado:\n%s", r.Text)
	}
}

func TestBrainPermFindWritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignora os bits de permissão")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("sem python3")
	}
	root := brFixture(t, "pw/open.txt", "x\n", "pw/locked.txt", "x\n")
	if err := os.Chmod(filepath.Join(root, "pw/locked.txt"), 0o000); err != nil {
		t.Skipf("sem chmod: %v", err)
	}
	t.Cleanup(func() { os.Chmod(filepath.Join(root, "pw/locked.txt"), 0o644) })
	env, _ := brTestEnv(t, root, nil, -1)
	r := brRun(context.Background(), env, "find all writable files")
	if r.Status != tkSucceeded {
		t.Fatalf("writable %s:\n%s", r.Status, r.Text)
	}
	if !strings.Contains(r.Text, "open.txt") || strings.Contains(r.Text, "locked.txt") {
		t.Errorf("writable não filtrou:\n%s", r.Text)
	}
	r = brRun(context.Background(), env, "find all files which are read-only")
	if r.Status != tkSucceeded {
		t.Fatalf("read-only %s:\n%s", r.Status, r.Text)
	}
	if !strings.Contains(r.Text, "locked.txt") || strings.Contains(r.Text, "open.txt") {
		t.Errorf("not_writable não filtrou:\n%s", r.Text)
	}
}

// list_files com referência temporal: a gramática pt plural não enche o
// slot (nlu.go congelado), mas quando o slot CHEGA — cadeia, aprendido,
// intent sintética — o método tem de honrar o filtro, não listar tudo.
func TestBrainListFilesTimeRef(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("sem python3")
	}
	root := brFixture(t, "ref.txt", "x\n", "old.txt", "x\n", "new.txt", "x\n")
	old := time.Now().Add(-48 * time.Hour)
	os.Chtimes(filepath.Join(root, "old.txt"), old, old)
	ref := time.Now().Add(-time.Hour)
	os.Chtimes(filepath.Join(root, "ref.txt"), ref, ref)
	env, _ := brTestEnv(t, root, nil, -1)

	in := Intent{Name: "list_files", Confidence: 0.97,
		Slots: map[string]string{"newer_file": "ref.txt", "type": "file"}}
	r := brRunIntent(context.Background(), env, "lista arquivos mais novos que ref.txt", in, brResult{})
	if r.Status != tkSucceeded {
		t.Fatalf("newer %s:\n%s", r.Status, r.Text)
	}
	if !strings.Contains(r.Text, "new.txt") || strings.Contains(r.Text, "old.txt") {
		t.Errorf("newer_file não filtrou:\n%s", r.Text)
	}

	in = Intent{Name: "list_files", Confidence: 0.97,
		Slots: map[string]string{"older_file": "ref.txt", "type": "file"}}
	r = brRunIntent(context.Background(), env, "lista arquivos mais velhos que ref.txt", in, brResult{})
	if r.Status != tkSucceeded {
		t.Fatalf("older %s:\n%s", r.Status, r.Text)
	}
	if !strings.Contains(r.Text, "old.txt") || strings.Contains(r.Text, "new.txt") {
		t.Errorf("older_file não filtrou:\n%s", r.Text)
	}

	// referência que não existe: falha honesta, nunca listagem sem filtro
	in = Intent{Name: "list_files", Confidence: 0.97,
		Slots: map[string]string{"newer_file": "missing.txt", "type": "file"}}
	r = brRunIntent(context.Background(), env, "lista arquivos mais novos que missing.txt", in, brResult{})
	if r.Status == tkSucceeded {
		t.Fatalf("referência inexistente deu verde:\n%s", r.Text)
	}
	if strings.Contains(r.Text, "new.txt") || strings.Contains(r.Text, "old.txt") {
		t.Errorf("caiu numa listagem sem o filtro pedido:\n%s", r.Text)
	}
}
