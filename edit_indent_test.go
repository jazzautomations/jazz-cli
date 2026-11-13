package main

import (
	"strings"
	"testing"
)

func TestEditIndentFlexibleGoTabs(t *testing.T) {
	src := "package x\n\nfunc f() {\n\tif a {\n\t\treturn 1\n\t}\n\treturn 2\n}\n"
	// the model copied with 4 spaces
	old := "    if a {\n        return 1\n    }"
	nw := "    if a && b {\n        return 1\n    }\n    if c {\n        return 3\n    }"
	res, err := applyEdits(src, []editOp{{Old: old, New: nw}}, "x.go")
	if err != nil {
		t.Fatal(err)
	}
	want := "package x\n\nfunc f() {\n\tif a && b {\n\t\treturn 1\n\t}\n\tif c {\n\t\treturn 3\n\t}\n\treturn 2\n}\n"
	if res.Content != want || !res.Indent {
		t.Errorf("got %q (indent=%v)", res.Content, res.Indent)
	}
}

func TestEditIndentFlexiblePythonLevel(t *testing.T) {
	src := "class A:\n    def f(self):\n        x = 1\n        return x\n"
	old := "def f(self):\n    x = 1\n    return x"
	nw := "def f(self):\n    x = 2\n    return x"
	res, err := applyEdits(src, []editOp{{Old: old, New: nw}}, "a.py")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "    def f(self):\n        x = 2\n        return x") {
		t.Errorf("got %q", res.Content)
	}
}

func TestEditIndentFlexibleRefusesAmbiguous(t *testing.T) {
	src := "def a():\n    x = 1\n    y = 2\n\ndef b():\n  x = 1\n  y = 2\n"
	if _, err := applyEdits(src, []editOp{{Old: "x = 1\ny = 2", New: "x = 3\ny = 2"}}, "a.py"); err == nil {
		t.Error("two indentation-flexible matches must stay an error")
	}
	if _, err := applyEdits(src, []editOp{{Old: "\t\tz = 9\n\t\tw = 1", New: "z"}}, "a.py"); err == nil {
		t.Error("no match must stay an error")
	}
}
