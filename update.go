package main

// update.go: `jazz update [--check]` and the daily background check (G19). Owner: platform.
//
// Sources:
//   - default: GitHub releases of pfUpdateRepo (assets jazz-<goos>-<goarch> + checksums.txt)
//   - config "update_url": either a GitHub-style release JSON (URL ending in .json),
//     or a plain directory served over HTTP (e.g. `python3 -m http.server` on the
//     Mac over the tailnet) holding VERSION, checksums.txt and jazz-<goos>-<goarch>.
// Every update must be signed (security review F7): the release carries
// manifest.txt — "jazz-cli release manifest v1", "version X.Y.Z" and the
// sha256 of each asset — and manifest.txt.sig, an ed25519 signature made by
// scripts/sign with the key whose public half is embedded at build time
// (-ldflags -X main.updatePubKey=<base64>, scripts/release.sh). jazz refuses
// to update when the binary has no key, the signature is missing or invalid,
// the signed version differs from the advertised one, or it is not newer than
// the running version (no replay of an old signed release under a new tag).
// Redirects are re-checked with pfUpdateURLOK. Plain-http sources are accepted
// only on loopback or the tailnet (100.64.0.0/10). The binary is then
// verified against the signed sha256 and replaces the running executable with
// an atomic rename in the same directory.

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const pfUpdateRepo = "jazzautomations/jazz-cli"

// updatePubKey is the base64 ed25519 public key release manifests are signed
// with, set at build time (scripts/release.sh). Empty = this build cannot
// self-update (development build).
var updatePubKey = ""

// pfManifestHeader is the first line of a signed release manifest.
const pfManifestHeader = "jazz-cli release manifest v1"

// pfVersionRe bounds what an update source may call a version: it is printed
// and cached, so it must not carry escapes or a page of text.
var pfVersionRe = regexp.MustCompile(`^v?[0-9]{1,9}(\.[0-9]{1,9}){0,3}([-+][0-9A-Za-z.-]{1,32})?$`)

var pfTailnet = &net.IPNet{IP: net.IPv4(100, 64, 0, 0).To4(), Mask: net.CIDRMask(10, 32)}

// pfUpdateURLOK rejects update URLs that anyone on the local network could
// rewrite: https is fine, http only for loopback and tailnet addresses.
func pfUpdateURLOK(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("update_url inválida: %w", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return nil
	case "http":
		h := u.Hostname()
		if h == "localhost" {
			return nil
		}
		if ip := net.ParseIP(h); ip != nil && (ip.IsLoopback() || pfTailnet.Contains(ip)) {
			return nil
		}
		return fmt.Errorf("recuso atualizar por http de %s: qualquer um na mesma rede poderia trocar o binário. Use https, o IP do tailnet (100.x) ou 127.0.0.1", h)
	}
	return fmt.Errorf("update_url precisa ser http(s): %s", raw)
}

// pfVerifySums checks sig (base64 ed25519) over data with the embedded key.
// A build without a key cannot verify anything, so it refuses.
func pfVerifySums(sums, sig []byte) error {
	if updatePubKey == "" {
		return errors.New("este binário não tem chave pública de atualização (build de desenvolvimento): instale uma release oficial com scripts/install.sh")
	}
	pk, err := base64.StdEncoding.DecodeString(strings.TrimSpace(updatePubKey))
	if err != nil || len(pk) != ed25519.PublicKeySize {
		return errors.New("chave pública de atualização inválida neste binário")
	}
	s, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sig)))
	if err != nil || len(s) != ed25519.SignatureSize || !ed25519.Verify(ed25519.PublicKey(pk), sums, s) {
		return errors.New("assinatura inválida: atualização abortada")
	}
	return nil
}

// pfManifest is a verified release manifest.
type pfManifest struct {
	Version string
	Sums    []byte // "<sha256>  <asset>" lines, for pfParseSums
}

// pfVerifyManifest checks the signature over manifest, then that it names the
// advertised version and that this version is newer than the running one.
func pfVerifyManifest(manifest, sig []byte, claimed, current string) (*pfManifest, error) {
	if err := pfVerifySums(manifest, sig); err != nil {
		return nil, err
	}
	lines := strings.Split(strings.ReplaceAll(string(manifest), "\r\n", "\n"), "\n")
	if len(lines) < 2 || strings.TrimSpace(lines[0]) != pfManifestHeader {
		return nil, errors.New("manifesto de release em formato desconhecido")
	}
	m := &pfManifest{}
	var sums strings.Builder
	for _, l := range lines[1:] {
		l = strings.TrimSpace(l)
		if v, ok := strings.CutPrefix(l, "version "); ok {
			m.Version = strings.TrimPrefix(strings.TrimSpace(v), "v")
		} else if l != "" {
			sums.WriteString(l + "\n")
		}
	}
	if m.Version == "" || !pfVersionRe.MatchString(m.Version) {
		return nil, errors.New("manifesto sem versão válida")
	}
	if claimed = strings.TrimPrefix(claimed, "v"); claimed != "" && claimed != m.Version {
		return nil, fmt.Errorf("a versão assinada (%s) não é a anunciada (%s): atualização abortada", m.Version, claimed)
	}
	if !pfVersionNewer(m.Version, current) {
		return nil, fmt.Errorf("a versão assinada %s não é mais nova que a atual %s: recuso voltar de versão", m.Version, current)
	}
	m.Sums = []byte(sums.String())
	return m, nil
}

// pfUpdateClient re-checks every redirect hop with pfUpdateURLOK, so an https
// URL cannot bounce the download to plain http on the local network.
var pfUpdateClient = &http.Client{
	Transport: httpClient.Transport,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("redirecionamentos demais")
		}
		return pfUpdateURLOK(req.URL.String())
	},
}

// pfRelease is what an update source offers for this platform.
type pfRelease struct {
	Version   string // without the leading v
	AssetURL  string
	SumsURL   string
	SigURL    string // checksums.txt.sig ("" = none offered)
	ManURL    string // manifest.txt (signed: version + sha256 of every asset)
	ManSigURL string // manifest.txt.sig
	AssetName string
}

func pfAssetName() string { return pfAssetNameFor(runtime.GOOS, runtime.GOARCH) }

// pfAssetNameFor is the release asset of a platform: jazz-<os>-<arch>, with
// .exe on Windows (jazz-windows-amd64.exe).
func pfAssetNameFor(goos, goarch string) string {
	n := "jazz-" + goos + "-" + goarch
	if goos == "windows" {
		n += ".exe"
	}
	return n
}

func pfHTTPGet(ctx context.Context, url string, max int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	if strings.Contains(url, "api.github.com") {
		req.Header.Set("Accept", "application/vnd.github+json")
	}
	resp, err := pfUpdateClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("%s: HTTP %d %s", url, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("%s: resposta maior que %d bytes", url, max)
	}
	return b, nil
}

// pfLatestRelease asks the configured source for the newest release.
func pfLatestRelease(ctx context.Context, cfg *Config) (*pfRelease, error) {
	src := ""
	if cfg != nil {
		src = strings.TrimSpace(cfg.UpdateURL)
	}
	asset := pfAssetName()
	if src != "" {
		if err := pfUpdateURLOK(src); err != nil {
			return nil, err
		}
	}
	if src != "" && !strings.HasSuffix(src, ".json") {
		base := strings.TrimSuffix(src, "/")
		b, err := pfHTTPGet(ctx, base+"/VERSION", 256)
		if err != nil {
			return nil, err
		}
		v := strings.TrimSpace(string(b))
		if !pfVersionRe.MatchString(v) {
			return nil, errors.New("VERSION inválida no servidor de atualização")
		}
		return &pfRelease{Version: strings.TrimPrefix(v, "v"), AssetURL: base + "/" + asset, SumsURL: base + "/checksums.txt", SigURL: base + "/checksums.txt.sig", ManURL: base + "/manifest.txt", ManSigURL: base + "/manifest.txt.sig", AssetName: asset}, nil
	}
	if src == "" {
		src = "https://api.github.com/repos/" + pfUpdateRepo + "/releases/latest"
	}
	b, err := pfHTTPGet(ctx, src, 4<<20)
	if err != nil {
		return nil, err
	}
	var rel struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(b, &rel); err != nil {
		return nil, fmt.Errorf("resposta de release inválida: %w", err)
	}
	r := &pfRelease{Version: strings.TrimPrefix(rel.TagName, "v"), AssetName: asset}
	for _, a := range rel.Assets {
		switch a.Name {
		case asset:
			r.AssetURL = a.URL
		case "checksums.txt", "SHA256SUMS", "sha256sums.txt":
			r.SumsURL = a.URL
		case "checksums.txt.sig", "SHA256SUMS.sig", "sha256sums.txt.sig":
			r.SigURL = a.URL
		case "manifest.txt":
			r.ManURL = a.URL
		case "manifest.txt.sig":
			r.ManSigURL = a.URL
		}
	}
	if r.Version == "" {
		return nil, errors.New("release sem tag_name")
	}
	if !pfVersionRe.MatchString(rel.TagName) {
		return nil, errors.New("tag_name inválida na release")
	}
	for _, u := range []string{r.AssetURL, r.SumsURL, r.SigURL, r.ManURL, r.ManSigURL} {
		if u != "" {
			if err := pfUpdateURLOK(u); err != nil {
				return nil, err
			}
		}
	}
	return r, nil
}

// pfVersionNewer reports whether a is newer than b (dotted numbers; suffixes ignored).
func pfVersionNewer(a, b string) bool {
	pa, pb := pfVersionParts(a), pfVersionParts(b)
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x != y {
			return x > y
		}
	}
	return false
}

func pfVersionParts(v string) []int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+ "); i >= 0 {
		v = v[:i]
	}
	var out []int
	for _, p := range strings.Split(v, ".") {
		n, _ := strconv.Atoi(p)
		out = append(out, n)
	}
	return out
}

// pfParseSums finds the sha256 of name in a checksums.txt ("<hex>  <name>").
func pfParseSums(b []byte, name string) string {
	sc := bufio.NewScanner(strings.NewReader(string(b)))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) >= 2 && strings.TrimPrefix(f[len(f)-1], "*") == name && len(f[0]) == 64 {
			return strings.ToLower(f[0])
		}
	}
	return ""
}

// pfInstallBinary verifies data against want and atomically replaces exe.
// When the directory is not writable it returns the command to run instead.
func pfInstallBinary(exe string, data []byte, want string) (hint string, err error) {
	h := sha256.Sum256(data)
	if got := hex.EncodeToString(h[:]); got != want {
		return "", fmt.Errorf("sha256 não confere (esperado %s, recebido %s): atualização abortada", want, got)
	}
	dir := filepath.Dir(exe)
	tmp, err := os.CreateTemp(dir, ".jazz-update-*")
	if err != nil {
		// not writable: leave the verified binary in a temp dir and explain
		f, err2 := os.CreateTemp("", "jazz-update-*")
		if err2 != nil {
			return "", err
		}
		f.Write(data)
		f.Close()
		os.Chmod(f.Name(), 0o755)
		if runtime.GOOS == "windows" {
			return fmt.Sprintf("sem permissão para escrever em %s. Num PowerShell como administrador rode:\n  Move-Item -Force %s %s\nou reinstale em %%LOCALAPPDATA%%\\Programs\\jazz (scripts/install.ps1)", dir, f.Name(), exe), nil
		}
		return fmt.Sprintf("sem permissão para escrever em %s. Rode:\n  sudo install -m755 %s %s\nou reinstale em ~/.local/bin (scripts/install.sh)", dir, f.Name(), exe), nil
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return "", err
	}
	tmp.Close()
	if err := os.Chmod(name, 0o755); err != nil {
		return "", err
	}
	if err := pfReplaceExe(name, exe); err != nil {
		return "", err
	}
	return "", nil
}

// pfReplaceExe moves the verified binary tmp over exe. Windows cannot
// overwrite a running .exe but can rename it: the old one moves to
// exe+".old" (deleted at the next start by pfCleanOldExe) and the new one
// takes its name; if that fails the old one is put back.
func pfReplaceExe(tmp, exe string) error {
	if runtime.GOOS != "windows" {
		return os.Rename(tmp, exe)
	}
	old := exe + ".old"
	_ = os.Remove(old)
	if err := os.Rename(exe, old); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(tmp, exe); err != nil {
		_ = os.Rename(old, exe)
		return err
	}
	return nil
}

// pfCleanOldExe removes the binary a Windows update left behind (best effort:
// it fails while that old process still runs, and is retried next time).
func pfCleanOldExe() {
	if runtime.GOOS != "windows" {
		return
	}
	if exe, err := os.Executable(); err == nil {
		_ = os.Remove(exe + ".old")
	}
}

// pfDownloadVerified fetches the signed manifest, verifies it (signature,
// version, no downgrade) and then the asset against the signed sha256.
func pfDownloadVerified(ctx context.Context, rel *pfRelease, current string) (data []byte, want string, err error) {
	if rel.AssetURL == "" {
		return nil, "", errors.New("a release " + rel.Version + " não tem binário " + rel.AssetName)
	}
	if updatePubKey == "" {
		return nil, "", pfVerifySums(nil, nil) // the "no key" error
	}
	if rel.ManURL == "" || rel.ManSigURL == "" {
		return nil, "", errors.New("a release " + rel.Version + " não tem manifest.txt assinado: recuso instalar binário não assinado")
	}
	man, err := pfHTTPGet(ctx, rel.ManURL, 1<<20)
	if err != nil {
		return nil, "", err
	}
	sig, err := pfHTTPGet(ctx, rel.ManSigURL, 4096)
	if err != nil {
		return nil, "", err
	}
	m, err := pfVerifyManifest(man, sig, rel.Version, current)
	if err != nil {
		return nil, "", err
	}
	want = pfParseSums(m.Sums, rel.AssetName)
	if want == "" {
		return nil, "", errors.New("o manifesto assinado não lista " + rel.AssetName)
	}
	fmt.Printf("baixando %s…\n", rel.AssetName)
	data, err = pfHTTPGet(ctx, rel.AssetURL, 64<<20)
	if err != nil {
		return nil, "", err
	}
	if got := pfHash(data); got != want {
		return nil, "", fmt.Errorf("sha256 não confere (esperado %s, recebido %s): atualização abortada", want, got)
	}
	return data, want, nil
}

// cmdUpdate implements `jazz update [--check]`.
func cmdUpdate(cfg *Config, args []string) {
	check := false
	for _, a := range args {
		if a == "--check" || a == "-n" {
			check = true
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	rel, err := pfLatestRelease(ctx, cfg)
	if err != nil {
		fatal("não consegui consultar atualizações: " + err.Error())
	}
	pfSaveUpdateCache(rel.Version)
	if !pfVersionNewer(rel.Version, version) {
		fmt.Printf("jazz %s já é a versão mais nova (%s disponível)\n", version, rel.Version)
		return
	}
	fmt.Printf("nova versão: %s (atual %s)\n", rel.Version, version)
	if check {
		fmt.Println("rode: jazz update")
		return
	}
	data, want, err := pfDownloadVerified(ctx, rel, version)
	if err != nil {
		auditRec("update", map[string]any{"from": version, "to": rel.Version, "signature": "refused", "error": oneLine(err.Error(), 300), "installed": false})
		fatal(err.Error())
	}
	exe, err := os.Executable()
	if err != nil {
		fatal(err.Error())
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	hint, err := pfInstallBinary(exe, data, want)
	// pfDownloadVerified only returns data whose signed manifest and sha256 checked out
	auditRec("update", map[string]any{"from": version, "to": rel.Version, "signature": "ok", "sha256": want, "exe": exe, "installed": err == nil && hint == ""}) // audit.go
	if err != nil {
		fatal(err.Error())
	}
	if hint != "" {
		fmt.Println(hint)
		os.Exit(1)
	}
	fmt.Printf("✓ jazz %s instalado em %s\n", rel.Version, exe)
}

// ---------- daily background check ----------

type pfUpdateCache struct {
	Checked int64  `json:"checked"`
	Latest  string `json:"latest"`
}

func pfUpdateCachePath() string { return filepath.Join(dataDir(), "update.json") }

func pfLoadUpdateCache() pfUpdateCache {
	var c pfUpdateCache
	if b, err := os.ReadFile(pfUpdateCachePath()); err == nil {
		_ = json.Unmarshal(b, &c)
	}
	return c
}

func pfSaveUpdateCache(latest string) {
	b, _ := json.Marshal(pfUpdateCache{Checked: time.Now().Unix(), Latest: latest})
	if os.MkdirAll(dataDir(), 0o700) == nil {
		_ = writeFileAtomic(pfUpdateCachePath(), b, 0o600)
	}
}

// pfUpdateNotice returns the dim "new version" line from the cached check
// ("" when up to date) and, at most once a day, refreshes the cache in the
// background for the next start. JAZZ_NO_UPDATE_CHECK=1 turns it off.
func pfUpdateNotice(cfg *Config) string {
	if os.Getenv("JAZZ_NO_UPDATE_CHECK") != "" {
		return ""
	}
	c := pfLoadUpdateCache()
	if time.Since(time.Unix(c.Checked, 0)) > 24*time.Hour {
		pfSaveUpdateCache(c.Latest) // claim today's check even if it fails (offline boxes)
		safeGo(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if rel, err := pfLatestRelease(ctx, cfg); err == nil {
				pfSaveUpdateCache(rel.Version)
			}
		})
	}
	if c.Latest != "" && pfVersionRe.MatchString(c.Latest) && pfVersionNewer(c.Latest, version) {
		return "nova versão " + c.Latest + " disponível — jazz update"
	}
	return ""
}
