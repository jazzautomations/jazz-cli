package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// rdT builds a deterministic random-looking string over alphabet (fixtures
// are assembled at run time so the test file itself holds no secret shapes).
func rdT(seed uint32, n int, alphabet string) string {
	b := make([]byte, n)
	x := seed*2654435761 + 1013904223
	for i := range b {
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		b[i] = alphabet[int(x%uint32(len(alphabet)))]
	}
	return string(b)
}

const (
	rdAlnum = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"
	rdUpNum = "ABCDEFGHJKLMNPQRSTUVWXYZ234567"
	rdHex   = "0123456789abcdef"
	rdB64   = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789+/"
	rdURLB  = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789_-"
)

// rdReset gives the test a fresh session redactor in mode m.
func rdReset(t *testing.T, m rdMode) {
	t.Helper()
	prevS, prevEv, prevLocal, prevRoot, prevKey := rdSess, rdEvent, rdLocal, rdRoot, rdMacKey
	rdSess = &Redactor{loaded: true, mode: m}
	rdEvent = func(string, string) {}
	rdLocal = func(string) bool { return false }
	dir := t.TempDir()
	rdRoot = func() string { return dir }
	rdMacKey = nil
	secretMu.Lock()
	prevVals := secretValues
	secretValues = nil
	secretMu.Unlock()
	t.Cleanup(func() {
		rdSess, rdEvent, rdLocal, rdRoot, rdMacKey = prevS, prevEv, prevLocal, prevRoot, prevKey
		secretMu.Lock()
		secretValues = prevVals
		secretMu.Unlock()
	})
}

type rdFixture struct {
	name   string
	text   string
	secret string // must not appear after redaction ("" = negative fixture)
	kind   string // expected placeholder kind
	strict bool   // only redacted in strict mode
}

func rdPEM(kind string, body string) string {
	return "-----BEGIN " + kind + "PRIVATE KEY-----\n" + body + "\n-----END " + kind + "PRIVATE KEY-----"
}

func rdFixtures() []rdFixture {
	akia := "AKIA" + rdT(1, 16, rdUpNum)
	awsSec := rdT(2, 40, rdB64)
	gcpID := rdT(3, 40, rdHex)
	pemBody := rdT(4, 64, rdB64) + "\n" + rdT(5, 64, rdB64) + "\n" + rdT(6, 20, rdB64) + "=="
	gapi := "AIza" + rdT(7, 35, rdURLB)
	ya := "ya29." + rdT(8, 60, rdURLB)
	azKey := rdT(9, 86, rdB64) + "=="
	azSig := rdT(10, 43, rdAlnum) + "%3D"
	azCli := rdT(11, 3, rdAlnum) + "8Q~" + rdT(12, 34, rdAlnum)
	ghp := "ghp_" + rdT(13, 36, rdAlnum)
	pat := "github_pat_" + rdT(14, 22, rdAlnum) + "_" + rdT(15, 59, rdAlnum)
	gho := "gho_" + rdT(16, 36, rdAlnum)
	ghs := "ghs_" + rdT(17, 36, rdAlnum)
	glpat := "glpat-" + rdT(18, 20, rdURLB)
	xoxb := "xoxb-" + rdT(19, 12, "0123456789") + "-" + rdT(20, 13, "0123456789") + "-" + rdT(21, 24, rdAlnum)
	slackHook := "https://hooks.slack.com/services/T" + rdT(22, 8, rdUpNum) + "/B" + rdT(23, 8, rdUpNum) + "/" + rdT(24, 24, rdAlnum)
	discord := "https://discord.com/api/webhooks/" + rdT(25, 18, "0123456789") + "/" + rdT(26, 68, rdURLB)
	stripe := "sk_live_" + rdT(27, 24, rdAlnum)
	whsec := "whsec_" + rdT(28, 32, rdAlnum)
	twilio := "SK" + rdT(29, 32, rdHex)
	sendgrid := "SG." + rdT(30, 22, rdURLB) + "." + rdT(31, 43, rdURLB)
	oaiProj := "sk-proj-" + rdT(32, 48, rdURLB)
	oaiOld := "sk-" + rdT(33, 48, rdAlnum)
	ant := "sk-ant-api03-" + rdT(34, 93, rdURLB) + "AA"
	orKey := "sk-or-v1-" + rdT(35, 64, rdHex)
	groq := "gsk_" + rdT(36, 52, rdAlnum)
	jwt := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJ" + rdT(37, 40, rdURLB) + "." + rdT(38, 43, rdURLB)
	bearer := rdT(39, 40, rdAlnum)
	basic := rdT(40, 28, rdB64) + "=="
	urlPass := rdT(41, 14, rdAlnum)
	pgPass := rdT(42, 12, rdAlnum)
	mongoPass := rdT(43, 16, rdAlnum)
	yamlPass := "Sup3r" + rdT(44, 6, rdAlnum) + "!"
	senha := "Mud4r@" + rdT(45, 6, rdAlnum)
	dbPass := "Pa55w0rd!" + rdT(46, 3, rdAlnum)
	mailgun := "key-" + rdT(47, 32, rdHex)
	svcTok := rdT(48, 32, rdAlnum)
	cliSec := rdT(49, 24, rdAlnum)
	apiKeyJSON := rdT(50, 32, rdAlnum)
	adoPass := "Xk82!" + rdT(51, 6, rdAlnum)
	npmTok := "npm_" + rdT(52, 36, rdAlnum)
	hfTok := "hf_" + rdT(53, 34, "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz")
	query := rdT(54, 24, rdAlnum)
	generic := rdT(55, 44, rdAlnum)
	asia := "ASIA" + rdT(56, 16, rdUpNum)
	return []rdFixture{
		// ---- positives (mode on) ----
		{"aws access key", "aws_access_key_id = " + akia, akia, "aws-key", false},
		{"aws temp key", "creds: " + asia + " (sts)", asia, "aws-key", false},
		{"aws secret", "aws_secret_access_key = " + awsSec, awsSec, "aws-secret", false},
		{"gcp sa key id", `{"type": "service_account", "private_key_id": "` + gcpID + `", "client_email": "x@y.iam.gserviceaccount.com"}`, gcpID, "gcp-sa-key", false},
		{"gcp sa private key (json escaped)", `{"private_key": "-----BEGIN PRIVATE KEY-----\n` + strings.ReplaceAll(pemBody, "\n", `\n`) + `\n-----END PRIVATE KEY-----\n"}`, strings.Split(pemBody, "\n")[0], "chave-privada", false},
		{"google api key", "const key = '" + gapi + "'", gapi, "google-api-key", false},
		{"google oauth token", "access: " + ya, ya, "google-oauth", false},
		{"azure storage conn", "DefaultEndpointsProtocol=https;AccountName=acct;AccountKey=" + azKey + ";EndpointSuffix=core.windows.net", azKey, "azure-key", false},
		{"azure sas url", "https://acct.blob.core.windows.net/c/f?sv=2022-11-02&se=2026-01-01&sp=r&sig=" + azSig, azSig, "azure-sas", false},
		{"azure client secret", "AZURE_CLIENT_SECRET=" + azCli, azCli, "azure-key", false},
		{"github ghp", "token " + ghp, ghp, "github-token", false},
		{"github fine-grained pat", "GH_PAT=" + pat, pat, "github-token", false},
		{"github oauth", "oauth: " + gho, gho, "github-token", false},
		{"github in remote url", "url = https://x-access-token:" + ghs + "@github.com/o/r.git", ghs, "github-token", false},
		{"gitlab pat", "PRIVATE-TOKEN: " + glpat, glpat, "gitlab-token", false},
		{"slack bot token", "SLACK=" + xoxb, xoxb, "slack-token", false},
		{"slack webhook", "webhook: " + slackHook, slackHook, "slack-webhook", false},
		{"discord webhook", "post to " + discord, discord, "discord-webhook", false},
		{"stripe live key", "Stripe::api_key = '" + stripe + "'", stripe, "stripe-key", false},
		{"stripe webhook secret", "endpoint_secret = " + whsec, whsec, "stripe-key", false},
		{"twilio api key", "twilio key " + twilio, twilio, "twilio-key", false},
		{"sendgrid", "SENDGRID=" + sendgrid, sendgrid, "sendgrid-key", false},
		{"openai project key", "client = OpenAI(api_key=\"" + oaiProj + "\")", oaiProj, "openai-key", false},
		{"openai legacy key", "key: " + oaiOld, oaiOld, "openai-key", false},
		{"anthropic key", "ANTHROPIC_API_KEY=" + ant, ant, "anthropic-key", false},
		{"openrouter key", "Authorization: Bearer " + orKey, orKey, "openrouter-key", false},
		{"groq key", "groq " + groq, groq, "groq-key", false},
		{"jwt", "cookie session=" + jwt + "; Path=/", jwt, "jwt", false},
		{"rsa pem", rdPEM("RSA ", pemBody), strings.Split(pemBody, "\n")[1], "chave-privada", false},
		{"openssh pem", "key:\n" + rdPEM("OPENSSH ", pemBody) + "\nmore", strings.Split(pemBody, "\n")[0], "chave-privada", false},
		{"ec pem truncated", "-----BEGIN EC PRIVATE KEY-----\n" + pemBody + "\n[... output cut ...]", strings.Split(pemBody, "\n")[2], "chave-privada", false},
		{"bearer header", "curl -H 'Authorization: Bearer " + bearer + "' https://api.x.dev", bearer, "bearer", false},
		{"basic header", "Authorization: Basic " + basic, basic, "basic-auth", false},
		{"url userinfo", "proxy=https://bob:" + urlPass + "@proxy.corp:3128", urlPass, "url-credencial", false},
		{"postgres dsn", "DATABASE_URL=postgres://app:" + pgPass + "@db:5432/prod", pgPass, "db-conexao", false},
		{"mongodb srv", "mongodb+srv://u:" + mongoPass + "@cluster0.x.mongodb.net/db", mongoPass, "db-conexao", false},
		{"yaml password", "db:\n  user: app\n  password: \"" + yamlPass + "\"\n", yamlPass, "senha", false},
		{"senha pt-BR", "senha=" + senha, senha, "senha", false},
		{"dotenv password", "DB_PASSWORD=" + dbPass, dbPass, "", false},
		{"dotenv _KEY high entropy", "MAILGUN_KEY=" + mailgun, mailgun, "env-secret", false},
		{"export token", "export SERVICE_TOKEN=" + svcTok, svcTok, "", false},
		{"client_secret yaml", "client_secret: " + cliSec, cliSec, "segredo", false},
		{"json apiKey", `{"apiKey": "` + apiKeyJSON + `", "region": "us"}`, apiKeyJSON, "segredo", false},
		{"ado.net conn", "Server=tcp:x.database.windows.net;Database=d;User ID=u;Password=" + adoPass + ";Encrypt=true", adoPass, "db-conexao", false},
		{"npm token", "//registry.npmjs.org/:_authToken=" + npmTok, npmTok, "", false},
		{"hf token", "HF " + hfTok, hfTok, "hf-token", false},
		{"query token", "GET /cb?code=1&access_token=" + query + "&state=2", query, "segredo", false},
		// ---- strict only ----
		{"secret camelCase word (strict)", "secret: mySecretValue", "mySecretValue", "segredo", true},
		{"password plain word (strict)", "password=correcthorse", "correcthorse", "senha", true},
		{"generic high entropy (strict)", "blob " + generic + " end", generic, "alta-entropia", true},
		// ---- negatives ----
		{"commit sha", "commit 3f786850e387550fdab836ed7e6dc881de23001b\nAuthor: x", "", "", false},
		{"git log oneline", "a94a8fe5ccb19ba61c4c0873d391e987982fbbd3 fix token refresh", "", "", false},
		{"uuid token", "token: 123e4567-e89b-12d3-a456-426614174000", "", "", false},
		{"ssh public key", "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl user@host", "", "", false},
		{"public key pem", "-----BEGIN PUBLIC KEY-----\nMFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAE" + rdT(60, 40, rdB64) + "\n-----END PUBLIC KEY-----", "", "", false},
		{"certificate", "-----BEGIN CERTIFICATE-----\nMIIB" + rdT(61, 60, rdB64) + "\n-----END CERTIFICATE-----", "", "", false},
		{"placeholder already there", "token: <secreto:github-token#3> and password=<secreto:senha#1>", "", "", false},
		{"changeme", "password: changeme", "", "", false},
		{"aws docs example", "AKIAIOSFODNN7EXAMPLE wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", "", "", false},
		{"your api key here", `api_key = "your_api_key_here"`, "", "", false},
		{"all same char", "token=xxxxxxxxxxxxxxxxxxxxxxxx and secret=aaaaaaaaaaaaaaaaaa", "", "", false},
		{"env lookup", `password = os.Getenv("DB_PASSWORD")`, "", "", false},
		{"go field copy", "\tPassword: cfg.Password,\n\tToken: opts.Token,", "", "", false},
		{"shell var", "token: ${GITHUB_TOKEN}\ncurl -H \"Authorization: Bearer $TOKEN\" x", "", "", false},
		{"max tokens", "max_tokens: 4096\nnum_tokens=128000", "", "", false},
		{"scikit-learn", "pip install scikit-learn; see sk-learn-docs-for-the-estimator-api", "", "", false},
		{"sha256 digest", "image@sha256:" + rdT(62, 64, rdHex), "", "", false},
		{"lockfile integrity", `"integrity": "sha512-` + rdT(63, 86, rdB64) + `=="`, "", "", false},
		{"ts type", "interface U { password: string; token?: string }", "", "", false},
		{"publishable key", "NEXT_PUBLIC_STRIPE_KEY=pk_live_" + rdT(64, 24, rdAlnum), "", "", false},
		{"go header read", `token := r.Header.Get("Authorization")`, "", "", false},
		{"bearer prose", "send the Bearer token in the header", "", "", false},
		{"plain url", "see https://github.com/org/repo and postgres://localhost/db", "", "", false},
		{"url with var password", "https://user:${PASS}@host/x and https://user:password@host", "", "", false},
		// red-team canaries: never redacted, so a leak still shows up verbatim at the sink
		{"canary auth.json", `{"anthropic":{"type":"api","key":"sk-ant-CANARY-AUTH-0000000000000000"}}`, "", "", false},
		{"canary aws", "AWS_SECRET_ACCESS_KEY=CANARY-AWS-SECRET", "", "", false},
		{"canary ssh", "-----BEGIN OPENSSH PRIVATE KEY-----\nCANARY-SSH-PRIVATE-KEY-BODY\n-----END OPENSSH PRIVATE KEY-----", "", "", false},
		{"canary prior session", `{"role":"user","content":"my token is CANARY-PRIOR-SESSION"}`, "", "", false},
	}
}

func TestRedactFixtures(t *testing.T) {
	fx := rdFixtures()
	if len(fx) < 50 {
		t.Fatalf("only %d fixtures", len(fx))
	}
	for _, mode := range []rdMode{rdOn, rdStrict} {
		for _, f := range fx {
			t.Run(mode.String()+"/"+f.name, func(t *testing.T) {
				rdReset(t, mode)
				out := rdSess.Text(f.text, "test")
				want := f.secret != "" && (!f.strict || mode == rdStrict)
				if !want {
					if f.secret == "" && out != f.text && mode == rdOn {
						t.Fatalf("negative fixture changed:\n in: %q\nout: %q", f.text, out)
					}
					if f.secret != "" && strings.Contains(out, "<secreto:") {
						t.Fatalf("strict-only fixture redacted in mode on: %q", out)
					}
					return
				}
				if strings.Contains(out, f.secret) {
					t.Fatalf("secret leaked:\n in: %q\nout: %q", f.text, out)
				}
				if f.kind != "" && !strings.Contains(out, "<secreto:"+f.kind+"#") {
					t.Fatalf("want kind %s, got %q", f.kind, out)
				}
			})
		}
	}
}

// Strict must not touch ordinary code and logs that mode on leaves alone.
func TestRedactStrictNegativesStayClean(t *testing.T) {
	rdReset(t, rdStrict)
	for _, f := range rdFixtures() {
		if f.secret != "" {
			continue
		}
		switch f.name {
		case "ssh public key", "public key pem", "certificate":
			continue // base64 bodies look random to the generic rule; strict accepts that
		}
		if out := rdSess.Text(f.text, "test"); out != f.text {
			t.Errorf("%s: strict changed %q -> %q", f.name, f.text, out)
		}
	}
}

func TestRedactStableAndRoundTrip(t *testing.T) {
	rdReset(t, rdOn)
	tok := "ghp_" + rdT(90, 36, rdAlnum)
	other := "ghp_" + rdT(91, 36, rdAlnum)
	a := rdSess.Text("GITHUB_TOKEN="+tok+"\n", "tool:read")
	b := rdSess.Text("again "+tok+" and "+other, "tool:bash")
	if a != "GITHUB_TOKEN=<secreto:github-token#1>\n" {
		t.Fatalf("first: %q", a)
	}
	if b != "again <secreto:github-token#1> and <secreto:github-token#2>" {
		t.Fatalf("stability: %q", b)
	}
	args := jsonArgs(map[string]any{"command": "curl -H 'Authorization: token <secreto:github-token#1>' https://api.github.com/user"})
	got := rdRestoreCalls([]ToolCall{{ID: "1", Name: "bash", Args: args}, {ID: "2", Name: "webfetch", Args: `{"url":"https://x/<secreto:github-token#1>"}`}})
	var m map[string]string
	if err := json.Unmarshal([]byte(got[0].Args), &m); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m["command"], "token "+tok+"'") {
		t.Fatalf("bash not restored: %q", m["command"])
	}
	if got[1].Args != `{"url":"https://x/<secreto:github-token#1>"}` {
		t.Fatalf("network tool must keep the placeholder: %q", got[1].Args)
	}
	// unknown placeholders and the < form
	r, ok := rdSess.Restore(`{"content":"<secreto:github-token#2> <secreto:aws-key#9>"}`)
	if !ok || !strings.Contains(r, other) || !strings.Contains(r, "<secreto:aws-key#9>") {
		t.Fatalf("restore: %q", r)
	}
}

func TestRedactRestoreJSONEscapesMultiline(t *testing.T) {
	rdReset(t, rdOn)
	pem := rdPEM("RSA ", rdT(92, 64, rdB64)+"\n"+rdT(93, 64, rdB64))
	out := rdSess.Text("cat id:\n"+pem+"\n", "tool:bash")
	if strings.Contains(out, "BEGIN RSA") || !strings.Contains(out, "<secreto:chave-privada#1>") {
		t.Fatalf("pem: %q", out)
	}
	got := rdRestoreCalls([]ToolCall{{Name: "write", Args: jsonArgs(map[string]any{"path": "k.pem", "content": "<secreto:chave-privada#1>\n"})}})
	var m map[string]string
	if err := json.Unmarshal([]byte(got[0].Args), &m); err != nil {
		t.Fatalf("restored args not valid JSON: %v %q", err, got[0].Args)
	}
	if m["content"] != pem+"\n" {
		t.Fatalf("content %q", m["content"])
	}
}

func TestRedactOffAndLocalProvider(t *testing.T) {
	rdReset(t, rdOff)
	tok := "ghp_" + rdT(94, 36, rdAlnum)
	if out := rdSess.Text(tok, "x"); out != tok {
		t.Fatal("off must not redact")
	}
	rdReset(t, rdOn)
	rdLocal = rdLocalBase
	req := ChatRequest{Messages: []Message{{Role: "user", Content: tok}}}
	if got := rdOutbound(req, "http://127.0.0.1:11434/v1"); got.Messages[0].Content != tok {
		t.Fatal("local provider must get the request as is")
	}
	if got := rdOutbound(req, "https://api.kilo.ai/api/openrouter"); got.Messages[0].Content == tok {
		t.Fatal("remote provider got the token")
	}
	if req.Messages[0].Content != tok {
		t.Fatal("rdOutbound must not mutate the caller's history")
	}
}

func TestRedactOutboundToolMetadataAndKnownRecall(t *testing.T) {
	rdReset(t, rdOn)
	tok := "ghp_" + rdT(95, 36, rdAlnum)
	r := rdSess
	first := r.Text("token="+tok, "tool")
	if strings.Contains(first, tok) {
		t.Fatal("fixture token was not detected")
	}
	// A previously recognized value remains redacted without the trigger word.
	if again := r.Text("echo "+tok, "tool"); strings.Contains(again, tok) || !strings.Contains(again, "<secreto:") {
		t.Fatalf("known value escaped without its original context: %q", again)
	}
	req := ChatRequest{Tools: []ToolSpec{{Name: "write", Description: "example " + tok, Params: map[string]any{
		"type": "object", "properties": map[string]any{"content": map[string]any{"type": "string", "description": tok, "enum": []string{tok}}},
	}}}}
	out := rdOutbound(req, "https://provider.example/v1")
	if strings.Contains(out.Tools[0].Description, tok) || strings.Contains(fmt.Sprint(out.Tools[0].Params), tok) {
		t.Fatal("tool metadata leaked a known secret")
	}
	if !strings.Contains(fmt.Sprint(out.Tools[0].Params), "<secreto:") {
		t.Fatal("schema string was not redacted")
	}
	if strings.Contains(req.Tools[0].Description, "<secreto:") {
		t.Fatal("outbound redaction mutated the caller's tool metadata")
	}
	// A context-free input cached before discovery must not remain a leak after
	// the value is recognized in a later message.
	other := "local-only-value"
	r.mu.Lock()
	r.byVal[other] = "<secreto:credencial-local#8>"
	r.cache["echo "+other] = "echo " + other
	r.cacheN = len("echo ") + len(other)
	r.mu.Unlock()
	if got := r.Text("echo "+other, "tool"); strings.Contains(got, other) {
		t.Fatalf("cached previously recognized value escaped: %q", got)
	}
	r.mu.Lock()
	r.byVal["abc"] = "<secreto:credencial-local#9>"
	r.mu.Unlock()
	if got := r.Text("abc", "tool"); got == "abc" {
		t.Fatal("short previously recognized value escaped")
	}
}

func TestRedactModeSettings(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	env := map[string]string{}
	get := func(k string) string { return env[k] }
	if rdLoadMode(p, get) != rdOn {
		t.Fatal("default must be on")
	}
	os.WriteFile(p, []byte(`{"redact":"strict"}`), 0o600)
	if rdLoadMode(p, get) != rdStrict {
		t.Fatal("config strict")
	}
	os.WriteFile(p, []byte(`{"redact":false}`), 0o600)
	if rdLoadMode(p, get) != rdOff {
		t.Fatal("config false = off")
	}
	env["JAZZ_REDACT"] = "on"
	if rdLoadMode(p, get) != rdOn {
		t.Fatal("env wins")
	}
	if _, ok := rdParse("loud"); ok {
		t.Fatal("bad value accepted")
	}
}

func TestRedactAllowlist(t *testing.T) {
	rdReset(t, rdOn)
	t.Setenv("JAZZ_HOME", t.TempDir())
	root := rdRoot()
	tok := "ghp_" + rdT(95, 36, rdAlnum)
	if out := rdSess.Text("x "+tok, "u"); out != "x <secreto:github-token#1>" {
		t.Fatalf("%q", out)
	}
	if _, err := rdSess.AllowPlaceholder(root, "<secreto:nope#1>"); err == nil {
		t.Fatal("unknown placeholder accepted")
	}
	kind, err := rdSess.AllowPlaceholder(root, "<secreto:github-token#1>")
	if err != nil || kind != "github-token" {
		t.Fatal(kind, err)
	}
	if out := rdSess.Text("y "+tok, "u"); out != "y "+tok {
		t.Fatalf("allowed value still redacted: %q", out)
	}
	b, _ := os.ReadFile(rdAllowPath(root))
	if strings.Contains(string(b), tok) || !strings.Contains(string(b), `"hmac"`) {
		t.Fatalf("allow file must hold an HMAC, not the value: %s", b)
	}
	// a fresh session (same machine key) honours the file
	rdSess = &Redactor{loaded: true, mode: rdOn}
	rdMacKey = nil
	if out := rdSess.Text("z "+tok, "u"); out != "z "+tok {
		t.Fatalf("allowlist not loaded: %q", out)
	}
	// a file shipped by a repository is useless without this machine's key
	os.WriteFile(rdKeyPath(), []byte(strings.Repeat("ab", 32)+"\n"), 0o600)
	rdSess = &Redactor{loaded: true, mode: rdOn}
	rdMacKey = nil
	if out := rdSess.Text("w "+tok, "u"); out == "w "+tok {
		t.Fatal("allowlist honoured with a different machine key")
	}
	if !secretPath(rdKeyPath()) {
		t.Fatal("redact.key must be a secret path (the model must not read it)")
	}
}

func TestRedactKnownLocalValues(t *testing.T) {
	rdReset(t, rdOn)
	v := "zq" + rdT(96, 30, "abcdefghijkmnopqrstuvwxyz") // lowercase: no shape rule fires
	secretMu.Lock()
	secretValues = []string{v}
	secretMu.Unlock()
	if out := rdSess.Text("env: X="+v, "tool:bash"); out != "env: X=<secreto:credencial-local#1>" {
		t.Fatalf("%q", out)
	}
}

func TestRedactEventsHaveNoValues(t *testing.T) {
	rdReset(t, rdOn)
	var evs []string
	rdEvent = func(kind, where string) { evs = append(evs, kind+"@"+where) }
	tok := "glpat-" + rdT(97, 20, rdURLB)
	rdSess.Text("a "+tok, "tool:read")
	rdSess.Text("b "+tok, "tool:read") // same secret: no second event
	if len(evs) != 1 || evs[0] != "gitlab-token@tool:read" {
		t.Fatalf("events %v", evs)
	}
	if n := rdTakeTurn(); n != 1 {
		t.Fatalf("turn count %d", n)
	}
	if n := rdTakeTurn(); n != 0 {
		t.Fatalf("turn count not reset: %d", n)
	}
	if s := rdNoticeText(); !strings.Contains(s, "1 segredo não saiu da máquina") {
		t.Fatalf("notice %q", s)
	}
	if s := rdNoticeText(); s != "" {
		t.Fatal("notice must be one-time")
	}
}

// Provider boundary: the body an OpenAI-compatible provider sends carries the
// placeholder, never the value, in every part (system, user, tool result,
// earlier tool-call arguments).
func TestRedactProviderRequestBody(t *testing.T) {
	rdReset(t, rdOn)
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	tok := "sk-ant-api03-" + rdT(98, 60, rdURLB)
	p := &OpenAIProvider{Name: "x", BaseURL: srv.URL + "/v1", Key: "k"}
	_, err := p.Chat(context.Background(), ChatRequest{Model: "m", System: "sys " + tok, Messages: []Message{
		{Role: "user", Content: "use " + tok},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "c", Name: "bash", Args: jsonArgs(map[string]any{"command": "echo " + tok})}}},
		{Role: "tool", ToolCallID: "c", ToolName: "bash", Content: tok + "\n[exit code 0]"},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw := body
	if strings.Contains(string(raw), tok) {
		t.Fatalf("token reached the provider: %s", raw)
	}
	if strings.Count(string(raw), "secreto:anthropic-key#1") < 4 {
		t.Fatalf("placeholder missing from some part: %s", raw)
	}
}

// End to end through the agent loop: a config file with a token is read, the
// model only sees the placeholder, uses it in a bash command, and the command
// runs with the real value.
func TestRedactAgentLoopRoundTrip(t *testing.T) {
	rdReset(t, rdOn)
	tok := "ghp_" + rdT(99, 36, rdAlnum)
	var sawPH string
	h := newHarness(t, func(n int, r recReq) fakeResp {
		switch n {
		case 0:
			return fakeResp{Calls: []fakeCall{{ID: "r1", Name: "read", Args: jsonArgs(map[string]any{"path": "config.yaml"})}}}
		case 1:
			res := r.toolResults()["r1"]
			if !strings.Contains(res, "marks a secret that stays on this machine") {
				t.Errorf("first placeholder must come with the usage hint: %q", res)
			}
			sawPH = regexp.MustCompile(`<secreto:[a-z-]+#\d+>`).FindString(res)
			if strings.Contains(res, tok) {
				sawPH = "LEAK"
			}
			return fakeResp{Calls: []fakeCall{{ID: "b1", Name: "bash", Args: jsonArgs(map[string]any{"command": "printf '%s' '" + sawPH + "' > used.txt && wc -c < used.txt"})}}}
		}
		if raw, _ := json.Marshal(r.Raw); strings.Contains(string(raw), tok) {
			sawPH = "LEAK-LATER"
		}
		return fakeResp{Text: "done"}
	}, nil)
	os.WriteFile(filepath.Join(h.root, "config.yaml"), []byte("github:\n  token: "+tok+"\n"), 0o644)
	if err := h.run(t, "use the token from config.yaml"); err != nil {
		t.Fatal(err)
	}
	if sawPH != "<secreto:github-token#1>" {
		t.Fatalf("model saw %q", sawPH)
	}
	b, _ := os.ReadFile(filepath.Join(h.root, "used.txt"))
	if string(b) != tok {
		t.Fatalf("bash ran with %q, want the real token", b)
	}
	for _, m := range h.ag.Msgs {
		for _, tc := range m.ToolCalls {
			if strings.Contains(tc.Args, tok) {
				t.Fatal("history must keep the placeholder form of the call")
			}
		}
	}
	if len(h.ui.turns) != 1 || h.ui.turns[0].Redacted != 1 {
		t.Fatalf("turn totals %+v", h.ui.turns)
	}
}

func TestRedactTotalsText(t *testing.T) {
	if rdTotalsText(1) != "1 segredo retido" || rdTotalsText(3) != "3 segredos retidos" {
		t.Fatal(rdTotalsText(1), rdTotalsText(3))
	}
	if !strings.Contains(uiTurnLine(Theme{}, TurnStats{Dur: time.Second, Redacted: 2}), "2 segredos retidos") {
		t.Fatal("totals line")
	}
}

// rdBenchText is ~10 KB of ordinary code/log text with two secrets in it.
func rdBenchText() string {
	var b strings.Builder
	for i := 0; b.Len() < 10<<10; i++ {
		fmt.Fprintf(&b, "func handler%d(w http.ResponseWriter, r *http.Request) { // key lookup %d\n", i, i)
		fmt.Fprintf(&b, "\ttoken := r.Header.Get(\"Authorization\"); cfg.Password = os.Getenv(\"DB_PASSWORD\")\n")
		fmt.Fprintf(&b, "\tlog.Printf(\"commit %s ok https://example.org/x?id=%d\")\n", rdT(uint32(i), 40, rdHex), i)
		if i == 20 {
			b.WriteString("\tapi_key = \"" + rdT(7, 32, rdAlnum) + "\"\n\tgh := \"ghp_" + rdT(8, 36, rdAlnum) + "\"\n")
		}
	}
	return b.String()
}

func BenchmarkRedactScan10KB(b *testing.B) {
	s := rdBenchText()
	b.SetBytes(int64(len(s)))
	for i := 0; i < b.N; i++ {
		if len(rdScan(s, rdHigh)) != 2 {
			b.Fatal("want 2 hits")
		}
	}
}

func BenchmarkRedactScan10KBStrict(b *testing.B) {
	s := rdBenchText()
	b.SetBytes(int64(len(s)))
	for i := 0; i < b.N; i++ {
		rdScan(s, rdMedium)
	}
}

// The budget: ≤ 1 ms per 10 KB on the dev Mac (generous: the race detector and
// slow CI runners are covered by the 10× margin used here).
func TestRedactScanBudget(t *testing.T) {
	s := rdBenchText()
	if n := len(rdScan(s, rdHigh)); n != 2 {
		t.Fatalf("bench text: want 2 hits, got %d", n)
	}
	r := testing.Benchmark(BenchmarkRedactScan10KB)
	if per := time.Duration(r.NsPerOp()); per > 10*time.Millisecond {
		t.Fatalf("scan of 10 KB took %s", per)
	}
}

// A typical source file (this repo's agent.go, first 10 KB): the common case.
func BenchmarkRedactScanSource10KB(b *testing.B) {
	src, err := os.ReadFile("agent.go")
	if err != nil {
		b.Skip(err)
	}
	s := string(src[:10<<10])
	b.SetBytes(int64(len(s)))
	for i := 0; i < b.N; i++ {
		rdScan(s, rdHigh)
	}
}
