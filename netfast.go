package main

// netfast.go: honest, fast feedback about the network for interactive use.
//
//   - WithPhase: postStream reports what a request is doing (conectando →
//     aguardando 1º token → gerando) so the status line can say it.
//   - A connect watchdog: a request that has not connected after
//     pvConnectSlow probes the network in parallel (1-packet TCP connects to
//     the proxy, or to 1.1.1.1 and the provider); when nothing answers the
//     request fails right away with errNoNet instead of waiting for the 20 s
//     dial timeout and then the next candidate's.
//   - Agent.NetOffline (interactive): a network-class failure confirmed by a
//     probe ends the turn with errNoNet within seconds, so the app switches to
//     offline mode (offline.go) instead of waiting minutes in bgNetWait.

import (
	"context"
	"errors"
	"net"
	"net/http/httptrace"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type pvPhaseKey struct{}

// WithPhase makes postStream report the request's phase to f.
func WithPhase(ctx context.Context, f func(phase string)) context.Context {
	if f == nil {
		return ctx
	}
	return context.WithValue(ctx, pvPhaseKey{}, f)
}

func pvPhaseOf(ctx context.Context) func(string) {
	f, _ := ctx.Value(pvPhaseKey{}).(func(string))
	if f == nil {
		return func(string) {}
	}
	return f
}

// pvConnectSlow: a request not connected after this gets a network probe.
var pvConnectSlow = 3 * time.Second

// pvProbeTimeout is the per-address connect timeout of a probe.
var pvProbeTimeout = 2 * time.Second

// errNoNet: no network at all (probe failed). Status 0, so olIsOffline and
// bgNetClass treat it as the network.
var errNoNet = &APIError{Status: 0, Body: "sem rede: nenhuma conexão com a internet (a sonda não alcançou nem o provedor nem 1.1.1.1)"}

// pvProxyFor is the HTTPS proxy host:port in the environment ("" = none).
func pvProxyFor() string {
	for _, e := range []string{"HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy"} {
		if v := strings.TrimSpace(os.Getenv(e)); v != "" {
			if !strings.Contains(v, "://") {
				v = "http://" + v
			}
			return bgHostPort(v)
		}
	}
	return ""
}

// pvProbeAddrs: with a proxy, only the proxy matters (every request goes
// through it); otherwise the anchor and the request's host.
func pvProbeAddrs(rawURL string) []string {
	if p := pvProxyFor(); p != "" {
		return []string{p}
	}
	addrs := []string{bgNetAnchor}
	if u, err := url.Parse(rawURL); err == nil && u.Hostname() != "" {
		if hp := bgHostPort(u.Scheme + "://" + u.Host); hp != "" && hp != bgNetAnchor {
			if bgLocalHost(hp) {
				return nil // a local server: not about the internet
			}
			addrs = append(addrs, hp)
		}
	}
	return addrs
}

// pvTrace reports connection progress to the phase callback; connected is
// set once a connection (to the server or the proxy) is up.
func pvTrace(ctx context.Context, phase func(string), connected *atomic.Bool) context.Context {
	return httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		GotConn: func(httptrace.GotConnInfo) {
			connected.Store(true)
		},
		WroteRequest: func(httptrace.WroteRequestInfo) {
			connected.Store(true)
			phase("aguardando 1º token")
		},
	})
}

// pvConnectWatch starts the connect watchdog for a request to rawURL; it
// cancels the request with errNoNet when the probe finds no network. stop
// ends it.
func pvConnectWatch(ctx context.Context, rawURL string, connected *atomic.Bool, phase func(string), cancel context.CancelCauseFunc) (stop func()) {
	addrs := pvProbeAddrs(rawURL)
	if len(addrs) == 0 {
		return func() {}
	}
	done := make(chan struct{})
	var once sync.Once
	slow, probe := pvConnectSlow, bgNetProbe // read once here: tests swap these globals
	go func() {
		defer func() { _ = recover() }()
		select {
		case <-done:
			return
		case <-ctx.Done():
			return
		case <-time.After(slow):
		}
		if connected.Load() {
			return
		}
		phase("conexão lenta — testando a rede")
		if probe(ctx, addrs) || connected.Load() {
			if !connected.Load() {
				phase("conectando (rede lenta)")
			}
			return
		}
		select {
		case <-done:
			return
		default:
		}
		phase("sem rede")
		dbg("netwait", map[string]any{"connect_watchdog": "no network", "addrs": addrs})
		cancel(errNoNet)
	}()
	return func() { once.Do(func() { close(done) }) }
}

// bgDialAny connects to every address in parallel (pvProbeTimeout each) and
// reports whether one connected; it returns at the first success.
func bgDialAny(ctx context.Context, addrs []string) bool {
	if len(addrs) == 0 {
		return false
	}
	pctx, cancel := context.WithTimeout(ctx, pvProbeTimeout)
	defer cancel()
	res := make(chan bool, len(addrs))
	for _, ad := range addrs {
		go func(ad string) {
			var d net.Dialer
			c, err := d.DialContext(pctx, "tcp", ad)
			if err == nil {
				c.Close()
			}
			res <- err == nil
		}(ad)
	}
	for range addrs {
		if <-res {
			return true
		}
	}
	return false
}

// acNetDown: the failure is network-class and a probe confirms there is no
// connectivity at all (interactive fast path; see Agent.NetOffline).
func (a *Agent) acNetDown(ctx context.Context, ref string, err error, cands []string) bool {
	if errors.Is(err, errNoNet) || err == errNoNet {
		return true
	}
	if !bgNetClass(err) || errors.Is(err, errStreamIdle) || ctx.Err() != nil {
		return false // a stall is the provider's, not the network's, until probed by bgNetWait
	}
	pname, _ := SplitModel(ref)
	if pc := a.cfg.Providers[pname]; pc != nil && (pc.Local || pc.Ollama || bgLocalHost(bgHostPort(pc.BaseURL))) {
		return false
	}
	return !bgNetProbe(ctx, a.bgNetAddrs(ref, cands))
}

// acFastFirst moves the first "fast"-tier free candidate of acceptable
// quality (prior Q within 8 points of the primary, and the scoreboard not
// saying otherwise) in front of a slower free primary. A person waits for
// the first token interactively; laguna's p50 first byte is ~8.5 s on the
// the Dell measurements, minimax-m3.1-flash about half of that.
func (a *Agent) acFastFirst(cands []string) []string {
	if len(cands) < 2 || !a.cfg.pvIsFreeish(cands[0]) || acHasTier(a.cfg.ModelSettings(cands[0]).Tiers, "fast") {
		return cands
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("JAZZ_INTERACTIVE_FAST"))) {
	case "0", "off", "false", "no":
		return cands
	}
	p := cands[0]
	pq, ps := a.cfg.ModelSettings(p).Q, bgScoreOf(p)
	for i := 1; i < len(cands); i++ {
		c := cands[i]
		ms := a.cfg.ModelSettings(c)
		if !a.cfg.pvIsFreeish(c) || !acHasTier(ms.Tiers, "fast") || ms.Q < pq-8 {
			continue
		}
		if t, ok := a.stalled[c]; ok && time.Since(t) < acStallPenalty {
			continue
		}
		if _, dead := ExhaustedUntil(c); dead {
			continue
		}
		cs := bgScoreOf(c)
		if ps.OK >= bgScoreMinObs && cs.OK >= bgScoreMinObs && cs.MS > 0.8*ps.MS {
			return cands // measured: not faster here
		}
		if cs.obs() >= bgScoreMinObs && ps.obs() >= bgScoreMinObs && cs.value() < ps.value()-0.15 {
			return cands // measured: clearly worse here
		}
		out := append([]string{c}, cands[:i]...)
		return append(out, cands[i+1:]...)
	}
	return cands
}

func acHasTier(tiers []string, t string) bool {
	for _, x := range tiers {
		if x == t {
			return true
		}
	}
	return false
}
