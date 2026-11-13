package main

import "testing"

func TestSbinPathAppended(t *testing.T) {
	all := func(string) bool { return true }
	none := func(string) bool { return false }
	cases := []struct {
		in, want string
		ex       func(string) bool
	}{
		{"/usr/local/bin:/usr/bin:/bin", "/usr/local/bin:/usr/bin:/bin:/usr/local/sbin:/usr/sbin:/sbin", all},
		{"/usr/bin:/usr/sbin:/bin:/sbin", "/usr/bin:/usr/sbin:/bin:/sbin:/usr/local/sbin", all},
		{"/usr/bin:/bin", "/usr/bin:/bin", none},
		{"/usr/sbin/:/usr/bin", "/usr/sbin/:/usr/bin:/usr/local/sbin:/sbin", all},
		{"", "/usr/local/sbin:/usr/sbin:/sbin", all},
	}
	for _, c := range cases {
		if got := spWithSbin(c.in, c.ex); got != c.want {
			t.Errorf("spWithSbin(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
