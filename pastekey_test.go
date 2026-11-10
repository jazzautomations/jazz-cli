package main

import (
	"os"
	"testing"
)

func TestPasteKeyClean(t *testing.T) {
	for in, want := range map[string]string{
		"\x1b[200~AIzaSyABC-123_def\x1b[201~": "AIzaSyABC-123_def",
		"[200~AIzaSyABC-123_def[201~":         "AIzaSyABC-123_def", // ESC já descartado pelo leitor
		"AIzaSyABC-123_def201~":               "AIzaSyABC-123_def", // só o rabo, como foi achado salvo
		"  sk-abc.def_ghi-123\r\n":            "sk-abc.def_ghi-123",
		"\"sk-abc\"":                          "sk-abc",
		"sk-ant-api03-xyz":                    "sk-ant-api03-xyz",
		"nvapi-AbC_1-2":                       "nvapi-AbC_1-2",
		"":                                    "",
	} {
		if got := pkCleanKey(in); got != want {
			t.Errorf("pkCleanKey(%q) = %q, quer %q", in, got, want)
		}
	}
	// a chave salva com o resto da colagem é lida limpa
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("JAZZ_HOME", "")
	if err := os.MkdirAll(configDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authPath(), []byte(`{"gemini":{"type":"api","key":"AIzaSyABC-123_def201~"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if c := LoadAuth()["gemini"]; c == nil || c.Key != "AIzaSyABC-123_def" {
		t.Errorf("chave lida do auth.json: %+v", c)
	}
	if !pkLooksPasted("AIza-x[201~") || pkLooksPasted("sk-ant-api03-2012") {
		t.Error("pkLooksPasted")
	}
}
