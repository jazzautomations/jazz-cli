//go:build !windows

package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestNetIfacesCardapio runs jazz-scripts/net_ifaces.py through the script tool on the
// real /sys/class/net of the test machine. It asserts shape and invariants, never
// addresses: the loopback is the only interface whose values are known in advance.
func TestNetIfacesCardapio(t *testing.T) {
	if _, err := os.Stat("/sys/class/net"); err != nil {
		t.Skip("no /sys/class/net")
	}
	tb, _ := scTestBox(t)

	maskedMAC := regexp.MustCompile(`^[0-9a-f]{2}:[0-9a-f]{2}:[0-9a-f]{2}:xx:xx:xx$`)
	fullMAC := regexp.MustCompile(`^[0-9a-f]{2}(:[0-9a-f]{2}){5}$`)

	// default: no lo, MACs masked, sorted by name, every row carries every field
	v, out := scCall(t, tb, "net_ifaces", nil)
	d := scDataOf(v)
	if !scOK(v) || d["truncated"] != false {
		t.Fatalf("net_ifaces: %v\n%s", v, out.Text)
	}
	ifaces, _ := d["ifaces"].([]any)
	prev := ""
	for _, it := range ifaces {
		m := it.(map[string]any)
		name, _ := m["name"].(string)
		if name == "lo" {
			t.Errorf("lo listed without include_lo")
		}
		if name <= prev {
			t.Errorf("ifaces not sorted: %q after %q", name, prev)
		}
		prev = name
		for _, k := range []string{"state", "up", "carrier", "mtu", "mac", "ipv4", "ipv6", "rx_bytes", "tx_bytes", "rx_errors", "tx_errors", "speed_mbps"} {
			if _, ok := m[k]; !ok {
				t.Errorf("%s: missing field %s", name, k)
			}
		}
		if mac, ok := m["mac"].(string); ok && !maskedMAC.MatchString(mac) {
			t.Errorf("%s: mac %q is not masked", name, mac)
		}
		if _, ok := m["ipv4"].([]any); !ok {
			t.Errorf("%s: ipv4 is not a list: %v", name, m["ipv4"])
		}
	}
	switch dr := d["default_route_iface"].(type) {
	case nil:
	case string:
		if dr == "lo" || dr == "" {
			t.Errorf("default_route_iface = %q", dr)
		}
	default:
		t.Errorf("default_route_iface = %v (%T)", dr, dr)
	}
	txt, _ := d["text"].(string)
	if !strings.Contains(txt, "iface(s) up") && !strings.Contains(txt, "no interface up") {
		t.Errorf("text: %q", txt)
	}
	if !strings.Contains(txt, "default via") && !strings.Contains(txt, "no default route") {
		t.Errorf("text has no default-route verdict: %q", txt)
	}

	// loopback: known values; an explicit name wins over include_lo
	v, _ = scCall(t, tb, "net_ifaces", map[string]any{"name": "lo"})
	d = scDataOf(v)
	ifaces, _ = d["ifaces"].([]any)
	if !scOK(v) || len(ifaces) != 1 {
		t.Fatalf("name=lo: %v", v)
	}
	lo := ifaces[0].(map[string]any)
	if lo["name"] != "lo" || lo["mac"] != nil || lo["up"] != true {
		t.Errorf("lo = %v", lo)
	}
	if mtu, _ := lo["mtu"].(float64); mtu < 1500 {
		t.Errorf("lo mtu = %v", lo["mtu"])
	}
	v4, _ := lo["ipv4"].([]any)
	has127 := false
	for _, a := range v4 {
		if a == "127.0.0.1" {
			has127 = true
		}
	}
	if !has127 {
		t.Errorf("lo ipv4 = %v, want 127.0.0.1 (from /proc/net/fib_trie)", v4)
	}
	if lo["rx_bytes"] == nil || lo["rx_packets"] == nil {
		t.Errorf("lo counters missing: %v", lo)
	}

	// include_lo + full_mac: lo present, MACs unmasked
	v, _ = scCall(t, tb, "net_ifaces", map[string]any{"include_lo": true, "full_mac": true})
	d = scDataOf(v)
	ifaces, _ = d["ifaces"].([]any)
	sawLo := false
	for _, it := range ifaces {
		m := it.(map[string]any)
		if m["name"] == "lo" {
			sawLo = true
		}
		if mac, ok := m["mac"].(string); ok && !fullMAC.MatchString(mac) {
			t.Errorf("%v: full_mac gave %q", m["name"], mac)
		}
	}
	if !sawLo {
		t.Errorf("include_lo=true but lo missing: %v", d["ifaces"])
	}

	// unknown name: ok=true, empty list, honest text (not a fake interface)
	v, _ = scCall(t, tb, "net_ifaces", map[string]any{"name": "no-such-iface-zz"})
	d = scDataOf(v)
	if !scOK(v) || len(d["ifaces"].([]any)) != 0 || !strings.Contains(d["text"].(string), "no interface matches") {
		t.Errorf("unknown name: %v", v)
	}
}
