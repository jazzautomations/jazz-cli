package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// "Sign in with ChatGPT" — same OAuth client the Codex CLI uses.
const (
	codexClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	codexIssuer   = "https://auth.openai.com"
	originator    = "jazz-cli"
)

type tokenResp struct {
	IDToken      string `json:"id_token"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

func postAuth(ctx context.Context, endpoint string, form url.Values, jsonBody any) (int, []byte, error) {
	var req *http.Request
	var err error
	if form != nil {
		req, err = http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(form.Encode()))
		if err == nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
	} else {
		b, _ := json.Marshal(jsonBody)
		req, err = http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(string(b)))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
		}
	}
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, b, nil
}

// LoginChatGPTDevice runs the device-code flow: works on a headless box.
func LoginChatGPTDevice(ctx context.Context, say func(string)) error {
	st, b, err := postAuth(ctx, codexIssuer+"/api/accounts/deviceauth/usercode", nil, map[string]string{"client_id": codexClientID})
	if err != nil {
		return err
	}
	if st == 404 {
		return errors.New("login por código de dispositivo não está habilitado — use: jazz login chatgpt --browser")
	}
	if st/100 != 2 {
		return fmt.Errorf("falha ao pedir código (HTTP %d): %s", st, trimBody(b))
	}
	var uc struct {
		DeviceAuthID string `json:"device_auth_id"`
		UserCode     string `json:"user_code"`
		UserCodeAlt  string `json:"usercode"`
		Interval     any    `json:"interval"`
	}
	if err := json.Unmarshal(b, &uc); err != nil {
		return fmt.Errorf("resposta inesperada: %s", trimBody(b))
	}
	code := uc.UserCode
	if code == "" {
		code = uc.UserCodeAlt
	}
	interval := 5
	switch v := uc.Interval.(type) {
	case float64:
		interval = int(v)
	case string:
		if n, e := strconv.Atoi(strings.TrimSpace(v)); e == nil {
			interval = n
		}
	}
	if interval < 2 {
		interval = 5
	}
	say(fmt.Sprintf("\n  1. Abra no celular ou no Mac:  %s\n  2. Digite o código:            %s\n\n  (expira em 15 min — esperando você autorizar…)\n", codexIssuer+"/codex/device", code))

	deadline := time.Now().Add(15 * time.Minute)
	for {
		if time.Now().After(deadline) {
			return errors.New("código expirou — rode o login de novo")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(interval) * time.Second):
		}
		st, b, err := postAuth(ctx, codexIssuer+"/api/accounts/deviceauth/token", nil,
			map[string]string{"device_auth_id": uc.DeviceAuthID, "user_code": code})
		if err != nil {
			continue // transient network error: keep polling
		}
		if st == 403 || st == 404 {
			continue // still pending
		}
		if st/100 != 2 {
			return fmt.Errorf("autorização falhou (HTTP %d): %s", st, trimBody(b))
		}
		var dt struct {
			AuthorizationCode string `json:"authorization_code"`
			CodeVerifier      string `json:"code_verifier"`
		}
		if err := json.Unmarshal(b, &dt); err != nil || dt.AuthorizationCode == "" {
			return fmt.Errorf("resposta inesperada: %s", trimBody(b))
		}
		return exchangeCode(ctx, dt.AuthorizationCode, dt.CodeVerifier, codexIssuer+"/deviceauth/callback")
	}
}

// LoginChatGPTBrowser runs the PKCE loopback flow on 127.0.0.1:1455.
// From another machine: ssh -L 1455:127.0.0.1:1455 <host>, then open the URL there.
func LoginChatGPTBrowser(ctx context.Context, say func(string)) error {
	verifier := randB64(64)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	state := randB64(32)

	var ln net.Listener
	var err error
	port := 0
	for _, p := range []int{1455, 1457} {
		if ln, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p)); err == nil {
			port = p
			break
		}
	}
	if ln == nil {
		return fmt.Errorf("portas 1455/1457 ocupadas: %w", err)
	}
	redirect := fmt.Sprintf("http://localhost:%d/auth/callback", port)
	q := url.Values{
		"response_type":              {"code"},
		"client_id":                  {codexClientID},
		"redirect_uri":               {redirect},
		"scope":                      {"openid profile email offline_access"},
		"code_challenge":             {challenge},
		"code_challenge_method":      {"S256"},
		"id_token_add_organizations": {"true"},
		"codex_cli_simplified_flow":  {"true"},
		"state":                      {state},
		"originator":                 {originator},
	}
	authURL := codexIssuer + "/oauth/authorize?" + q.Encode()
	say(fmt.Sprintf("\n  Abra este link no navegador (se estiver por SSH: ssh -L %d:127.0.0.1:%d <maquina>):\n\n  %s\n\n  Esperando o retorno…\n", port, port, authURL))

	type result struct {
		code string
		err  error
	}
	ch := make(chan result, 1)
	var once sync.Once
	// Only a request with the right state and a code (or an explicit error) ends the
	// wait. Anything else (a stray local process, a web page hitting localhost) gets
	// 400 and the login keeps waiting (bug 58).
	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth/callback" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		if q.Get("state") != state {
			http.Error(w, "state inválido", http.StatusBadRequest)
			return
		}
		res := result{}
		switch {
		case q.Get("error") != "":
			res.err = fmt.Errorf("login recusado: %s", q.Get("error_description"))
		case q.Get("code") == "":
			http.Error(w, "código ausente", http.StatusBadRequest)
			return
		default:
			res.code = q.Get("code")
		}
		if res.err != nil {
			fmt.Fprintln(w, "Falhou:", res.err)
		} else {
			fmt.Fprintln(w, "Pronto! Pode fechar esta aba e voltar pro terminal.")
		}
		once.Do(func() { ch <- res })
	})}
	go srv.Serve(ln)
	defer srv.Close()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(10 * time.Minute):
		return errors.New("tempo esgotado esperando o login")
	case res := <-ch:
		if res.err != nil {
			return res.err
		}
		return exchangeCode(ctx, res.code, verifier, redirect)
	}
}

func exchangeCode(ctx context.Context, code, verifier, redirect string) error {
	st, b, err := postAuth(ctx, codexIssuer+"/oauth/token", url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {codexClientID},
		"code":          {code},
		"redirect_uri":  {redirect},
		"code_verifier": {verifier},
	}, nil)
	if err != nil {
		return err
	}
	var tr tokenResp
	_ = json.Unmarshal(b, &tr)
	if st/100 != 2 || tr.AccessToken == "" {
		return fmt.Errorf("troca de código falhou (HTTP %d): %s", st, trimBody(b))
	}
	cr := &Cred{Type: "oauth", Access: tr.AccessToken, Refresh: tr.RefreshToken, IDToken: tr.IDToken, LastRefresh: time.Now().UnixMilli()}
	cr.Expires = jwtExpiry(tr.AccessToken)
	cr.AccountID = accountIDFrom(tr.IDToken, tr.AccessToken)
	if cr.AccountID == "" {
		return errors.New("login ok, mas não achei o account id da conta ChatGPT no token")
	}
	return SaveCred("chatgpt", cr)
}

var refreshMu sync.Mutex

// chatgptWarn holds a non-fatal problem from the last refresh (save failed), shown by main.
var chatgptWarn string

// ChatGPTWarning returns and clears the last non-fatal refresh warning.
func ChatGPTWarning() string {
	refreshMu.Lock()
	defer refreshMu.Unlock()
	w := chatgptWarn
	chatgptWarn = ""
	return w
}

// chatgptCred returns a valid credential, refreshing it when close to expiry (or forced).
// The refresh runs under an flock on configDir()/auth.lock so two jazz processes never
// spend the same (rotating) refresh token; after taking the lock auth.json is re-read
// and a refresh another process already did is reused (bug 35).
func chatgptCred(ctx context.Context, force bool) (*Cred, error) {
	refreshMu.Lock()
	defer refreshMu.Unlock()
	cr := LoadAuth()["chatgpt"]
	if cr == nil || cr.Refresh == "" {
		return nil, errors.New("não logado no ChatGPT — rode: jazz login chatgpt")
	}
	now := time.Now().UnixMilli()
	if !force && !pvCredStale(cr, now) {
		return cr, nil
	}
	seen := cr.LastRefresh
	if err := os.MkdirAll(configDir(), 0o700); err == nil {
		if unlock, err := pvFlock(filepath.Join(configDir(), "auth.lock"), 45*time.Second); err == nil {
			defer unlock()
			if fresh := LoadAuth()["chatgpt"]; fresh != nil && fresh.Refresh != "" {
				if fresh.LastRefresh > seen && fresh.Expires-time.Now().UnixMilli() > 60*1000 {
					return fresh, nil // another process refreshed while we waited
				}
				cr = fresh
			}
		}
	}
	tr, st, b, err := pvRefresh(ctx, cr.Refresh)
	if err != nil {
		if !force && cr.Expires-now > 0 {
			return cr, nil // network hiccup but token still valid
		}
		return nil, err
	}
	if (st == 400 || st == 401) && strings.Contains(string(b), "invalid_grant") {
		// someone else may have rotated the token just now: re-read once
		if fresh := LoadAuth()["chatgpt"]; fresh != nil && fresh.Refresh != "" && fresh.Refresh != cr.Refresh {
			if fresh.Expires-time.Now().UnixMilli() > 60*1000 {
				return fresh, nil
			}
			cr = fresh
			tr, st, b, err = pvRefresh(ctx, cr.Refresh)
			if err != nil {
				return nil, err
			}
		}
	}
	if st/100 != 2 || tr.AccessToken == "" {
		msg := trimBody(b)
		if st == 400 || st == 401 {
			return nil, fmt.Errorf("sessão do ChatGPT expirou (%s) — rode: jazz login chatgpt", msg)
		}
		return nil, fmt.Errorf("refresh falhou (HTTP %d): %s", st, msg)
	}
	cr.Access = tr.AccessToken
	if tr.RefreshToken != "" {
		cr.Refresh = tr.RefreshToken
	}
	if tr.IDToken != "" {
		cr.IDToken = tr.IDToken
		if id := accountIDFrom(tr.IDToken, tr.AccessToken); id != "" {
			cr.AccountID = id
		}
	}
	cr.Expires = jwtExpiry(cr.Access)
	cr.LastRefresh = now
	if err := SaveCred("chatgpt", cr); err != nil {
		// The old refresh token is already revoked: keep working with the new one.
		chatgptWarn = "não consegui salvar o token novo do ChatGPT (" + err.Error() + "); esta sessão continua, mas talvez precise de jazz login chatgpt depois"
		dbg("oauth", chatgptWarn)
	}
	return cr, nil
}

func pvCredStale(cr *Cred, now int64) bool {
	return cr.Expires == 0 || cr.Expires-now < 5*60*1000 || now-cr.LastRefresh > 8*24*3600*1000
}

func pvRefresh(ctx context.Context, refresh string) (tokenResp, int, []byte, error) {
	st, b, err := postAuth(ctx, codexIssuer+"/oauth/token", nil, map[string]string{
		"client_id": codexClientID, "grant_type": "refresh_token", "refresh_token": refresh,
	})
	var tr tokenResp
	if err == nil {
		_ = json.Unmarshal(b, &tr)
	}
	return tr, st, b, err
}

func jwtClaims(tok string) map[string]any {
	parts := strings.Split(tok, ".")
	if len(parts) < 2 {
		return nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	return m
}

func jwtExpiry(tok string) int64 {
	if c := jwtClaims(tok); c != nil {
		if e, ok := c["exp"].(float64); ok {
			return int64(e) * 1000
		}
	}
	return time.Now().Add(time.Hour).UnixMilli()
}

func accountIDFrom(tokens ...string) string {
	for _, t := range tokens {
		c := jwtClaims(t)
		if c == nil {
			continue
		}
		if v, ok := c["chatgpt_account_id"].(string); ok && v != "" {
			return v
		}
		if a, ok := c["https://api.openai.com/auth"].(map[string]any); ok {
			if v, ok := a["chatgpt_account_id"].(string); ok && v != "" {
				return v
			}
		}
		if orgs, ok := c["organizations"].([]any); ok && len(orgs) > 0 {
			if o, ok := orgs[0].(map[string]any); ok {
				if v, ok := o["id"].(string); ok && v != "" {
					return v
				}
			}
		}
	}
	return ""
}

func randB64(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func trimBody(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}
