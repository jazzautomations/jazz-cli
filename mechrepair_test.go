package main

import "testing"

// TestMrParseGoErrors: a sample `go build` output with 2 diagnostics
// yields 2 structs with the right File/Line/Col; noise lines are skipped.
func TestMrParseGoErrors(t *testing.T) {
	out := "# example.com/p\n" +
		"./a.go:5:2: \"os\" imported and not used\n" +
		"./b.go:9:12: declared and not used: z\n" +
		"\thave (number, number)\n" +
		"\twant (int)\n"
	errs := mrParseGoErrors(out)
	if len(errs) != 2 {
		t.Fatalf("got %d errors, want 2: %#v", len(errs), errs)
	}
	if errs[0].File != "./a.go" || errs[0].Line != 5 || errs[0].Col != 2 {
		t.Errorf("err0 = %#v", errs[0])
	}
	if errs[1].File != "./b.go" || errs[1].Line != 9 || errs[1].Col != 12 {
		t.Errorf("err1 = %#v", errs[1])
	}
	if errs[1].Msg != "declared and not used: z" {
		t.Errorf("err1.Msg = %q", errs[1].Msg)
	}
}

// TestMrClassify: each common message maps to its recipe; an unknown
// message (type mismatch) maps to "".
func TestMrClassify(t *testing.T) {
	cases := []struct{ msg, want string }{
		{`"os" imported and not used`, "remove-import"},
		{"declared and not used: z", "remove-var"},
		{"undefined: undefinedThing", "missing-decl"},
		{"syntax error: unexpected }", "syntax"},
		{"missing go.sum entry for module providing package x", "go-mod-tidy"},
		{"not enough arguments in call to g", "signature"},
		{"too many arguments in call to g", "signature"},
		{"cannot use x (variable of type int) as string value in return statement", ""},
	}
	for _, c := range cases {
		if got := mrClassify(c.msg); got != c.want {
			t.Errorf("mrClassify(%q) = %q, want %q", c.msg, got, c.want)
		}
	}
}

// TestMrRecipe: tool-fixable kinds return a command; kinds needing a
// manual edit return "".
func TestMrRecipe(t *testing.T) {
	for _, k := range []string{"remove-import", "go-mod-tidy"} {
		if mrRecipe(k) == "" {
			t.Errorf("mrRecipe(%q) = \"\", want a deterministic command", k)
		}
	}
	for _, k := range []string{"missing-decl", "remove-var", "syntax", "signature", "nonsense"} {
		if got := mrRecipe(k); got != "" {
			t.Errorf("mrRecipe(%q) = %q, want \"\"", k, got)
		}
	}
}
