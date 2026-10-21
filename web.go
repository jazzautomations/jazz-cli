package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"syscall"
	"time"
)

var fetchSpec = ToolSpec{Name: "fetch", Description: "Fetch a web page or API as readable text (HTML is converted to plain text, JSON pretty-printed), or search the web with query. Use it for docs, changelogs and error messages instead of curl.",
	Params: obj(map[string]any{
		"url":       str("http(s) URL to fetch"),
		"query":     str("Web search query (used when url is empty)"),
		"max_chars": integer("Maximum characters to return (default 30000)"),
	})}

var errFetchBlocked = errors.New("blocked: private, loopback or tailnet address")

// fetchCGNAT is 100.64.0.0/10: carrier-grade NAT and every Tailscale node.
var fetchCGNAT = &net.IPNet{IP: net.IPv4(100, 64, 0, 0).To4(), Mask: net.CIDRMask(10, 32)}

// fetchIPBlocked reports addresses the model must never reach through fetch.
func fetchIPBlocked(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() || ip.IsMulticast() || fetchCGNAT.Contains(ip) || ip.Equal(net.IPv4bcast)
}

// fetchDialControl runs after DNS resolution, right before connecting, so it
// also covers names that resolve (or rebind, or redirect) to a private address.
func fetchDialControl(_, addr string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if fetchIPBlocked(net.ParseIP(host)) {
		return errFetchBlocked
	}
	return nil
}

// fetchClient has its own transport: no proxy (the proxy would be the one
// dialled, defeating the check) and a dialer that refuses private addresses.
// The shared httpClient keeps reaching ollama and other local model servers.
var fetchClient = &http.Client{Timeout: 25 * time.Second, Transport: auTransport("fetch", &http.Transport{
	Proxy:                 nil,
	DialContext:           (&net.Dialer{Timeout: 20 * time.Second, KeepAlive: 30 * time.Second, Control: fetchDialControl}).DialContext,
	ForceAttemptHTTP2:     true,
	MaxIdleConns:          4,
	IdleConnTimeout:       60 * time.Second,
	TLSHandshakeTimeout:   20 * time.Second,
	ResponseHeaderTimeout: 25 * time.Second,
}), CheckRedirect: func(req *http.Request, via []*http.Request) error {
	if len(via) >= 6 {
		return fmt.Errorf("too many redirects")
	}
	if r := fetchBlocked(req.URL.String()); r != "" {
		return fmt.Errorf("redirect blocked: %s", r)
	}
	return nil
}}

// fetchBlocked returns a reason when a URL must not be fetched silently.
func fetchBlocked(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "invalid URL"
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "only http(s) URLs are allowed"
	}
	host := u.Hostname()
	if host == "" {
		return "missing host"
	}
	if host == "localhost" || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
		return "local address"
	}
	if ip := net.ParseIP(host); ip != nil && fetchIPBlocked(ip) {
		return "private/loopback address"
	}
	return ""
}

func fetchTool(ctx context.Context, m map[string]any, raw string) ToolOutput {
	u, _ := argStr(m, "url")
	q, _ := argStr(m, "query")
	max, ok := argInt(m, "max_chars")
	if !ok || max <= 0 {
		max = maxToolBytes
	}
	if max > 200000 {
		max = 200000
	}
	u, q = strings.TrimSpace(u), strings.TrimSpace(q)
	if u == "" && q == "" {
		return validationErr("fetch", "provide url or query", raw)
	}
	if u == "" {
		return webSearch(ctx, q, max)
	}
	if !strings.Contains(u, "://") {
		u = "https://" + u
	}
	if r := fetchBlocked(u); r != "" {
		return fetchBlockedOut(u, r)
	}
	t0 := time.Now()
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return ToolOutput{IsErr: true, Text: "bad URL: " + err.Error()}
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; jazz-cli/"+version+")")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/json,text/plain,text/markdown,*/*;q=0.8")
	req.Header.Set("Accept-Language", "pt-BR,pt;q=0.9,en;q=0.8")
	resp, err := fetchClient.Do(req)
	if err != nil {
		// a name that resolves (or redirects) to a private address: a block,
		// not a network failure the model should investigate
		if errors.Is(err, errFetchBlocked) || strings.Contains(err.Error(), "redirect blocked") || strings.Contains(err.Error(), errFetchBlocked.Error()) {
			return fetchBlockedOut(u, "it resolves or redirects to a private, loopback or tailnet address")
		}
		return ToolOutput{IsErr: true, Text: "fetch failed: " + err.Error(), Display: Display{Summary: firstSentence(err.Error())}}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 5<<20))
	ct := resp.Header.Get("Content-Type")
	var text, suspect, hiddenNote string
	switch {
	case strings.Contains(ct, "json") || json.Valid(body) && len(body) > 0 && (body[0] == '{' || body[0] == '['):
		var v any
		if json.Unmarshal(body, &v) == nil {
			b, _ := json.MarshalIndent(v, "", "  ")
			text = string(b)
		} else {
			text = string(body)
		}
	case strings.Contains(ct, "html") || looksHTML(body):
		// hidden elements (display:none, white-on-white …) never reach the
		// model; instructions inside them flag the result (sanitize.go)
		page, hidden := snHiddenHTML(string(body), true)
		text = htmlToText(page)
		if suspect = (snReport{Hidden: hidden}).suspect(); suspect != "" {
			hiddenNote = "\n\n[jazz: HTML escondido com instruções removido da página]"
		}
	default:
		if isBinary(body) {
			return ToolOutput{IsErr: true, Text: fmt.Sprintf("%s is binary content (%s, %s). Use bash curl -o to download it.", u, ct, humanBytes(int64(len(body))))}
		}
		text = string(body)
	}
	text = strings.TrimSpace(text)
	if text == "" && hiddenNote != "" {
		// nothing left once the hidden instructions were removed: say so in one
		// line instead of an empty page that invites probing the server
		return ToolOutput{Text: fmt.Sprintf("HTTP %d from %s: the page only had hidden text with instructions for AI agents, which was removed; there is no visible content. %s", resp.StatusCode, u, blockSteer),
			Suspect: suspect, Display: Display{Summary: "só texto escondido (removido)"}}
	}
	if text == "" {
		text = "(empty body)"
	}
	status := ""
	if resp.StatusCode != 200 {
		status = fmt.Sprintf("HTTP %d\n", resp.StatusCode)
	}
	out := status + truncateHeadTail(text, max) + hiddenNote
	lines := nonEmptyLines(text)
	prev := lines
	if len(prev) > 3 {
		prev = prev[:3]
	}
	return ToolOutput{Text: out, Suspect: suspect, IsErr: resp.StatusCode >= 400, Display: Display{Summary: fmt.Sprintf("HTTP %d · %s · %s", resp.StatusCode, humanBytes(int64(len(text))), time.Since(t0).Round(100*time.Millisecond)), Preview: prev}}
}

// fetchBlockedOut is the one-line result of a fetch the guard refused.
func fetchBlockedOut(u, why string) ToolOutput {
	return ToolOutput{IsErr: true, Text: blockMsg("fetch "+u, "the fetch tool never reaches private, loopback or local addresses ("+why+")"),
		Display: Display{Summary: "bloqueado: " + why}, Blocked: "fetch para endereço local/privado (" + why + ")"}
}

func looksHTML(b []byte) bool {
	h := strings.ToLower(string(b[:min(len(b), 2048)]))
	return strings.Contains(h, "<html") || strings.Contains(h, "<!doctype html") || strings.Contains(h, "<body")
}

func isBinary(b []byte) bool {
	n := min(len(b), 4096)
	for i := 0; i < n; i++ {
		if b[i] == 0 {
			return true
		}
	}
	return false
}

var (
	reDropBlocks = dropBlockRes("script", "style", "noscript", "svg", "template", "iframe", "nav", "footer", "aside", "head")
	reComments   = regexp.MustCompile(`(?s)<!--.*?-->`)
	reHeading    = regexp.MustCompile(`(?is)<h([1-6])\b[^>]*>(.*?)</h[1-6]\s*>`)
	reAnchor     = regexp.MustCompile(`(?is)<a\b[^>]*?href\s*=\s*["']([^"'#][^"']*)["'][^>]*>(.*?)</a\s*>`)
	reLi         = regexp.MustCompile(`(?is)<li\b[^>]*>`)
	reBlock      = regexp.MustCompile(`(?is)<(/?)(p|div|br|hr|tr|section|article|header|main|ul|ol|table|blockquote|pre|h[1-6]|dt|dd|figcaption|summary|details|form)\b[^>]*>`)
	reCell       = regexp.MustCompile(`(?is)</t[dh]\s*>`)
	reTag        = regexp.MustCompile(`(?s)<[^>]+>`)
	reBlankRuns  = regexp.MustCompile(`\n[ \t]*(\n[ \t]*)+`)
	reSpaces     = regexp.MustCompile(`[ \t\x{00a0}]{2,}`)
)

// dropBlockRes builds one regexp per tag (RE2 has no backreferences like </\1>).
func dropBlockRes(tags ...string) []*regexp.Regexp {
	var out []*regexp.Regexp
	for _, t := range tags {
		out = append(out, regexp.MustCompile(`(?is)<`+t+`\b[^>]*>.*?</`+t+`\s*>`))
	}
	return out
}

// htmlToText is a dependency-free HTML → readable text conversion.
func htmlToText(s string) string {
	s = reComments.ReplaceAllString(s, "")
	for _, re := range reDropBlocks {
		s = re.ReplaceAllString(s, "\n")
	}
	s = reHeading.ReplaceAllStringFunc(s, func(m string) string {
		p := reHeading.FindStringSubmatch(m)
		return "\n\n" + strings.Repeat("#", int(p[1][0]-'0')) + " " + reTag.ReplaceAllString(p[2], "") + "\n\n"
	})
	s = reAnchor.ReplaceAllStringFunc(s, func(m string) string {
		p := reAnchor.FindStringSubmatch(m)
		txt := strings.TrimSpace(reTag.ReplaceAllString(p[2], ""))
		href := strings.TrimSpace(p[1])
		if txt == "" || strings.HasPrefix(href, "javascript:") {
			return txt
		}
		if strings.HasPrefix(href, "http") && !strings.EqualFold(txt, href) && len(href) < 160 {
			return txt + " (" + href + ")"
		}
		return txt
	})
	s = reLi.ReplaceAllString(s, "\n- ")
	s = reCell.ReplaceAllString(s, " | ")
	s = reBlock.ReplaceAllString(s, "\n")
	s = reTag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	var out []string
	for _, l := range strings.Split(s, "\n") {
		out = append(out, strings.TrimSpace(reSpaces.ReplaceAllString(l, " ")))
	}
	s = strings.Join(out, "\n")
	s = reBlankRuns.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

var (
	reDDGLink    = regexp.MustCompile(`(?is)<a[^>]+class=["']result-link["'][^>]*href=["']([^"']+)["'][^>]*>(.*?)</a>|<a[^>]+href=["']([^"']+)["'][^>]*class=["']result-link["'][^>]*>(.*?)</a>`)
	reDDGSnippet = regexp.MustCompile(`(?is)<td[^>]+class=["']result-snippet["'][^>]*>(.*?)</td>`)
)

func webSearch(ctx context.Context, q string, max int) ToolOutput {
	t0 := time.Now()
	form := url.Values{"q": {q}, "kl": {"br-pt"}}
	req, err := http.NewRequestWithContext(ctx, "POST", "https://lite.duckduckgo.com/lite/", strings.NewReader(form.Encode()))
	if err != nil {
		return ToolOutput{IsErr: true, Text: err.Error()}
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux i686) jazz-cli/"+version)
	resp, err := fetchClient.Do(req)
	if err != nil {
		return ToolOutput{IsErr: true, Text: "search failed: " + err.Error(), Display: Display{Summary: firstSentence(err.Error())}}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode != 200 {
		return ToolOutput{IsErr: true, Text: fmt.Sprintf("search failed: HTTP %d", resp.StatusCode)}
	}
	links := reDDGLink.FindAllStringSubmatch(string(body), -1)
	snips := reDDGSnippet.FindAllStringSubmatch(string(body), -1)
	var b strings.Builder
	fmt.Fprintf(&b, "Search results for %q:\n\n", q)
	n := 0
	for i, l := range links {
		href, title := l[1], l[2]
		if href == "" {
			href, title = l[3], l[4]
		}
		if u, err := url.Parse(href); err == nil {
			if real := u.Query().Get("uddg"); real != "" {
				href = real
			}
		}
		title = strings.TrimSpace(html.UnescapeString(reTag.ReplaceAllString(title, "")))
		if title == "" || strings.Contains(href, "duckduckgo.com/y.js") {
			continue
		}
		n++
		fmt.Fprintf(&b, "%d. %s\n   %s\n", n, title, href)
		if i < len(snips) {
			sn := strings.TrimSpace(html.UnescapeString(reTag.ReplaceAllString(snips[i][1], "")))
			if sn != "" {
				fmt.Fprintf(&b, "   %s\n", oneLine(sn, 240))
			}
		}
		b.WriteString("\n")
		if n >= 8 {
			break
		}
	}
	if n == 0 {
		return ToolOutput{IsErr: true, Text: "No results (the search page may have changed or blocked the request). Try fetch with a direct URL.", Display: Display{Summary: "sem resultados"}}
	}
	return ToolOutput{Text: truncateHeadTail(b.String(), max), Display: Display{Summary: fmt.Sprintf("%d resultados · %s", n, time.Since(t0).Round(100*time.Millisecond))}}
}
