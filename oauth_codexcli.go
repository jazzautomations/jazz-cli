package main

// Borrowing a Codex CLI login: `codex login` and `jazz login chatgpt` use the
// same OpenAI OAuth client, so a machine that already runs the Codex CLI has
// a valid ChatGPT session in $CODEX_HOME/auth.json (~/.codex). When jazz has
// no ChatGPT credential of its own it borrows that ACCESS token read-only.
// It never refreshes it: OpenAI rotates refresh tokens, and a refresh here
// would log the Codex CLI out. Expired = say so (run codex once, or log in).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

func codexCLIAuthPath() string {
	if h := os.Getenv("CODEX_HOME"); h != "" {
		return filepath.Join(h, "auth.json")
	}
	if os.Getenv("JAZZ_HOME") != "" {
		return "" // an isolated jazz home (tests, sandboxes) borrows nothing from the real ~
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex", "auth.json")
}

// codexCLICred returns the Codex CLI's ChatGPT session as a jazz Cred, or nil.
func codexCLICred() *Cred {
	p := codexCLIAuthPath()
	if p == "" {
		return nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var a struct {
		Tokens struct {
			IDToken      string `json:"id_token"`
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			AccountID    string `json:"account_id"`
		} `json:"tokens"`
	}
	if json.Unmarshal(b, &a) != nil || a.Tokens.AccessToken == "" {
		return nil
	}
	cr := &Cred{Type: "oauth", Access: a.Tokens.AccessToken, IDToken: a.Tokens.IDToken, AccountID: a.Tokens.AccountID,
		Expires: jwtExpiry(a.Tokens.AccessToken), LastRefresh: time.Now().UnixMilli()}
	if cr.AccountID == "" {
		cr.AccountID = accountIDFrom(a.Tokens.IDToken, a.Tokens.AccessToken)
	}
	return cr
}
