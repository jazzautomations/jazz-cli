//go:build !windows

package main

import (
	"os"
	"strings"
	"testing"
)

// TestNetRouteCardapio runs jazz-scripts/net_route.py through the script tool on the
// real /proc: a default route (if any) sorts first, every row carries dst/iface/
// family, and the answer never invents a gateway for on-link routes.
func TestNetRouteCardapio(t *testing.T) {
	tb, _ := scTestBox(t)
	if _, err := os.Stat("/proc/net/route"); err != nil {
		t.Skip("no /proc on this system")
	}

	v, out := scCall(t, tb, "net_route", map[string]any{})
	d := scDataOf(v)
	if !scOK(v) || scConf(v) < 0.9 {
		t.Fatalf("net_route: %v\n%s", v, out.Text)
	}
	routes, _ := d["routes"].([]any)
	if len(routes) != int(d["count"].(float64)) {
		t.Fatalf("count mismatch: %v", d)
	}
	seenDefault := false
	for i, r := range routes {
		m := r.(map[string]any)
		for _, k := range []string{"dst", "iface", "family", "default"} {
			if _, ok := m[k]; !ok {
				t.Fatalf("route row missing %s: %v", k, m)
			}
		}
		if m["family"] != "ipv4" && m["family"] != "ipv6" {
			t.Errorf("family: %v", m)
		}
		if m["default"] == true {
			seenDefault = true
			if i != 0 && routes[0].(map[string]any)["default"] != true {
				t.Errorf("default route not first: %v", routes)
			}
		}
	}
	txt, _ := d["text"].(string)
	if seenDefault && !strings.Contains(txt, "default via") {
		t.Errorf("text missing default: %q", txt)
	}
	if !seenDefault && !strings.Contains(txt, "no default route") {
		t.Errorf("text should admit no default: %q", txt)
	}
}
