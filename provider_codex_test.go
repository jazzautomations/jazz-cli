package main

import "testing"

// Guard: the Codex backend gates models by client version. Reporting jazz's
// own version made every model answer "not supported" and /models return
// empty — the chatgpt provider was dead end-to-end. Pin that the wire
// headers carry codexClientVersion, never `version`.
func TestCodexHeadersUseClientVersion(t *testing.T) {
	h := codexHeaders(&Cred{Access: "acc", AccountID: "a1"}, "s1")
	if h["version"] != codexClientVersion {
		t.Fatalf("version header = %q, want codexClientVersion %q", h["version"], codexClientVersion)
	}
	if h["version"] == version {
		t.Fatalf("version header must not be jazz's own version %q", version)
	}
	if codexClientVersion == "" {
		t.Fatal("codexClientVersion is empty")
	}
	if h["session-id"] != "s1" || h["ChatGPT-Account-ID"] != "a1" || h["Authorization"] != "Bearer acc" {
		t.Fatalf("headers: %v", h)
	}
}
