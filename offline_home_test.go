package main

import "testing"

// Every button runs a canonical request the offline brain understands with
// confidence on its own (no model, no confirmation), and no button is a
// "brain" pick (home clicks must never reach the forge as labels).
func TestOfflineHomeButtons(t *testing.T) {
	m, w := nuTestWorld(t)
	for _, s := range []string{"menu", "Menu", "início", "botões", "?", "o que dá pra fazer"} {
		if !ohIsHome(s) {
			t.Errorf("%q should open the menu", s)
		}
	}
	for _, s := range []string{"menu do restaurante no config.yaml", "lista as portas"} {
		if ohIsHome(s) {
			t.Errorf("%q must not open the menu", s)
		}
	}
	home := ohHome()
	if len(home) < 4 || len(home) > 6 {
		t.Fatalf("home has %d buttons", len(home))
	}
	for _, it := range home {
		if it.Kind == "brain" || it.Menu != "" {
			t.Errorf("home button %q is a forge-visible pick", it.Label)
		}
	}
	for _, g := range ohGroups {
		items, title := ohGroupItems(g.key)
		if title == "" || len(items) < 2 || items[len(items)-1].Kind != "submenu" {
			t.Fatalf("group %s malformed", g.key)
		}
		for _, it := range items[:len(items)-1] {
			if it.Kind != "home" {
				t.Errorf("%s/%s kind %s", g.key, it.Label, it.Kind)
			}
			in := m.Parse(it.Cmd, w)
			if !in.Understood() || in.Confidence < brActMin(in) {
				t.Errorf("button %s/%q → %q reads as %s %.2f (needs ≥ %.2f)", g.key, it.Label, it.Cmd, in.Name, in.Confidence, brActMin(in))
			}
		}
	}
}
