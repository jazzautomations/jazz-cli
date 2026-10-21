package main

// netwait.go: network-outage detection for the fallback chain. On the Dell the
// WiFi drops several times an hour for 3-5 minutes; every candidate then fails
// with a dial/DNS error or stalls, the chain burns through all of them (and
// marks them stalled), and the run ends with "model did not answer". Eval v2
// failures 02 and 10 (first run) were exactly that.
//
// Now a network-class failure (HTTP status 0: dial, DNS, reset, TLS; or a
// stall) triggers a cheap probe: a TCP connect to 1.1.1.1:443, to the host of
// every remote candidate in the chain (at most bgNetMaxHosts) and to the
// HTTPS proxy, if one is set. Any of them connects: the network is fine (the
// provider itself is failing), the normal fallback logic goes on. None: no
// candidate could answer either, so we wait with backoff, probing, until the
// network is back (then the same candidate is retried, with no stall penalty)
// or until the run's deadline (--timeout cancels ctx), capped at bgNetMaxWait.
// A successful probe is trusted for bgNetFresh, so a flaky provider costs at
// most one probe every 20 s.
//
// Config `net_wait: on|off` (default on), env JAZZ_NET_WAIT=0|1.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"
)

var (
	// bgNetProbe reports whether any addr accepts a TCP connection (tests replace it).
	bgNetProbe = bgDialAny
	// bgNetBackoff are the waits between probes during an outage (last one repeats).
	bgNetBackoff = []time.Duration{3 * time.Second, 6 * time.Second, 12 * time.Second, 20 * time.Second, 30 * time.Second}
	// bgNetMaxWait caps one outage wait when ctx has no deadline (interactive).
	bgNetMaxWait = 30 * time.Minute
	// bgNetFresh: a successful probe this recent skips the next one.
	bgNetFresh = 20 * time.Second
	// bgNetSkipLocal: failures of loopback/LAN providers never mean an outage
	// (tests turn it off to drive the wait through a local fake server).
	bgNetSkipLocal = true
)

// bgNetMaxRounds caps the outage waits of one candidate inside one request.
const bgNetMaxRounds = 8

// bgNetMaxHosts caps the provider hosts probed besides the anchor.
const bgNetMaxHosts = 4

// bgNetAnchor is probed besides the provider host (an IP: no DNS needed).
const bgNetAnchor = "1.1.1.1:443"

func (a *Agent) netWaitOn() bool {
	v := strings.ToLower(strings.TrimSpace(a.cfg.NetWait))
	if e := strings.TrimSpace(strings.ToLower(os.Getenv("JAZZ_NET_WAIT"))); e != "" {
		v = e
	}
	return v != "off" && v != "0" && v != "false"
}

// bgNetClass: the error looks like the network, not the provider (status 0
// transport failures and stalls).
func bgNetClass(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, errStreamIdle) {
		return true
	}
	var ae *APIError
	if errors.As(err, &ae) {
		if ae.Status != 0 {
			return false
		}
		b := strings.ToLower(ae.Body)
		return !strings.Contains(b, "grande demais") // our own size caps are not the network
	}
	var ne net.Error
	return errors.As(err, &ne)
}

// bgNetWait is called after a failed request to ref. When the failure looks
// like the network and a probe confirms there is no connectivity, it waits
// until the network is back and returns true (retry the same candidate).
// It returns true too when ctx ended while waiting (the caller checks ctx).
func (a *Agent) bgNetWait(ctx context.Context, ref string, err error, cands []string) bool {
	if !bgNetClass(err) || !a.netWaitOn() || ctx.Err() != nil {
		return false
	}
	pname, _ := SplitModel(ref)
	pc := a.cfg.Providers[pname]
	if pc != nil && (pc.Local || pc.Ollama) {
		return false // a local server being down is not an outage
	}
	st := a.bg()
	st.netMu.Lock()
	fresh := time.Since(time.Unix(0, st.netUp)) < bgNetFresh
	st.netMu.Unlock()
	if fresh {
		return false
	}
	if pc != nil && bgNetSkipLocal && bgLocalHost(bgHostPort(pc.BaseURL)) {
		return false // loopback/LAN server: not the internet
	}
	addrs := a.bgNetAddrs(ref, cands)
	if bgNetProbe(ctx, addrs) {
		st.netMu.Lock()
		st.netUp = time.Now().UnixNano()
		st.netMu.Unlock()
		return false
	}
	t0 := time.Now()
	dbg("netwait", map[string]any{"ref": ref, "err": oneLine(err.Error(), 200)})
	a.ui.Warn("sem conexão com a internet — esperando a rede voltar (até o --timeout; Esc cancela)")
	limit := bgNetMaxWait
	if dl, ok := ctx.Deadline(); ok {
		limit = time.Until(dl)
	}
	for i := 0; ; i++ {
		wait := bgNetBackoff[min(i, len(bgNetBackoff)-1)]
		a.ui.Thinking(fmt.Sprintf("sem rede há %s — testando de novo", time.Since(t0).Round(time.Second)))
		select {
		case <-ctx.Done():
			a.ui.StopThinking()
			return true
		case <-time.After(wait):
		}
		a.ui.StopThinking()
		if bgNetProbe(ctx, addrs) {
			break
		}
		if ctx.Err() != nil {
			return true
		}
		if time.Since(t0) >= limit {
			a.ui.Warn(fmt.Sprintf("a rede não voltou em %s — desisti de esperar", time.Since(t0).Round(time.Second)))
			return false
		}
	}
	st.netMu.Lock()
	st.netUp = time.Now().UnixNano()
	st.netMu.Unlock()
	// stalls recorded while the network was down say nothing about the models
	for r, at := range a.stalled {
		if !at.Before(t0.Add(-2 * time.Minute)) {
			delete(a.stalled, r)
		}
	}
	dbg("netwait", map[string]any{"ref": ref, "back_after_s": time.Since(t0).Seconds()})
	a.ui.Notice(fmt.Sprintf("a rede voltou depois de %s — tentando de novo", time.Since(t0).Round(time.Second)))
	return true
}

// bgNetAddrs: the HTTPS proxy alone when one is set, else the anchor and the remote hosts of ref and the
// other candidates (deduplicated, at most bgNetMaxHosts hosts).
func (a *Agent) bgNetAddrs(ref string, cands []string) []string {
	// with an HTTPS proxy every request goes through it: it alone decides
	if p := pvProxyFor(); p != "" {
		return []string{p}
	}
	addrs := []string{bgNetAnchor}
	seen := map[string]bool{bgNetAnchor: true}
	addHP := func(hp string) {
		if hp != "" && !seen[hp] {
			seen[hp] = true
			addrs = append(addrs, hp)
		}
	}
	hosts := 0
	for _, r := range append([]string{ref}, cands...) {
		if hosts >= bgNetMaxHosts {
			break
		}
		pname, _ := SplitModel(r)
		pc := a.cfg.Providers[pname]
		if pc == nil {
			continue
		}
		hp := bgHostPort(pc.BaseURL)
		if hp == "" || bgLocalHost(hp) || seen[hp] {
			continue
		}
		addHP(hp)
		hosts++
	}
	return addrs
}

// bgHostPort turns a base URL into host:port for a TCP probe ("" = none).
func bgHostPort(base string) string {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil || u.Hostname() == "" || strings.Contains(u.Host, "{") {
		return ""
	}
	port := u.Port()
	if port == "" {
		port = "443"
		if u.Scheme == "http" {
			port = "80"
		}
	}
	return net.JoinHostPort(u.Hostname(), port)
}

// bgLocalHost: host:port names localhost or a loopback/private/link-local IP.
func bgLocalHost(hp string) bool {
	h, _, err := net.SplitHostPort(hp)
	if err != nil {
		return false
	}
	if h == "localhost" || strings.HasSuffix(h, ".local") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast())
}
