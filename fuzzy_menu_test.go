//go:build !windows

package main

import "testing"

// An ambiguous typo becomes a numbered menu; the number runs the chosen line.
func TestFuzzyMenu(t *testing.T) {
	app, _ := olTestApp(t)
	app.command("/ofline") // offline vs online tie
	if len(app.menu) != 2 || app.menu[0].Kind != "slash" || app.menu[0].Cmd != "/offline" || app.menu[1].Cmd != "/online" {
		t.Fatalf("menu: %+v", app.menu)
	}
	act, ok := app.olTakePick("2")
	if !ok || act.Cmd != "/online" {
		t.Fatalf("pick 2: %+v %v", act, ok)
	}
	// arguments survive in the menu lines
	app.command("/mod x")
	if len(app.menu) < 2 || app.menu[0].Cmd != "/mode x" {
		t.Errorf("args lost: %+v", app.menu)
	}
	app.menu = nil
	// a known command opens no menu
	app.command("/todo")
	if app.menu != nil {
		t.Errorf("unexpected menu: %+v", app.menu)
	}
}
