package main

import "testing"

// Discarding output and quoted metacharacters do not make a command a
// change; system diagnostics read only when their subcommand reads
// (5.451 real commands: +334 read-only just by the /dev/null redirect).
func TestReadOnlyRedirectsQuotesDiagnostics(t *testing.T) {
	ro := []string{
		"ls 2>/dev/null", "cat a.txt >/dev/null 2>&1", "grep -rn foo . 2>&1 | head", "git status &>/dev/null",
		"rg x >&2", "ls 2>&-", "grep 'a|b' f", `grep "x > y" f`, "rg -n 'func (a' .", "grep -E 'foo$' f",
		"dmesg | tail -50", "dmesg -T | grep -i firmware", "ip a", "ip -br addr show", "ip route", "ip route get 1.1.1.1", "ip link",
		"iw dev", "iw dev wlan0 link", "iw list", "iwconfig", "iwconfig wlan0", "lspci -k", "lsusb", "lsmod | grep ipw",
		"modinfo ipw2200", "rfkill list", "rfkill", "journalctl -b -u NetworkManager --no-pager | tail -40", "systemctl status NetworkManager",
		"systemctl is-active sshd", "systemctl list-units --failed", "ss -tlnp", "lsblk", "findmnt /", "lsblk -f 2>/dev/null",
	}
	for _, c := range ro {
		if !IsReadOnly(c) {
			t.Errorf("IsReadOnly(%q) = false, want true", c)
		}
	}
	change := []string{
		"ls > out.txt", "ls 2>err.log", "ls >/dev/null/../x", "cat a >> /etc/hosts", `grep "$(rm x)" f`, "grep \"`rm x`\" f",
		"cat <(rm x)", "grep 'a' f > out", "echo 'x' > f", "grep 'unterminated f",
		"ip link set wlan0 up", "ip addr add 10.0.0.2/24 dev eth0", "ip route del default", "ip netns exec x sh", "ip -b cmds.txt", "ip -force link",
		"iw dev wlan0 set power_save off", "iw dev wlan0 connect casa", "iw dev wlan0 scan", "iwconfig wlan0 essid casa",
		"rfkill unblock wifi", "rfkill block all", "dmesg -C", "dmesg -c", "dmesg --clear", "dmesg -n 1",
		"journalctl --vacuum-time=1d", "journalctl --rotate", "journalctl --flush", "journalctl --cursor-file=/tmp/c",
		"systemctl restart NetworkManager", "systemctl enable sshd", "systemctl -H host status x", "systemctl --machine=c status x",
		"ss -K dst 1.2.3.4", "ss --kill", "hostname newname", "sysctl -w net.ipv4.ip_forward=1", "modprobe ipw2200",
		// a quoted action flag is still the flag (the quote mask must not hide it)
		"find . '-exec' rm x ';'", "find . '-exec' rm '{}' ';'", "find . '-ok' rm ';'", "find . '-delete'", "find . -fprint'out'",
		"sort -o'out' f", "git branch '-D' x",
	}
	for _, c := range change {
		if IsReadOnly(c) {
			t.Errorf("IsReadOnly(%q) = true, want false", c)
		}
	}
	// what the read-only diagnostics must not hide: the critical system
	// changes are still critical (menor intervenção)
	for _, c := range []string{"sysctl -w net.ipv4.ip_forward=1", "modprobe -r ipw2200", "sudo tee /etc/modprobe.d/ipw.conf"} {
		if cbSystemCritical("bash", nil, c) == "" {
			t.Errorf("%q must stay critical", c)
		}
	}
}

// env/printenv print every API key of the process: they are never
// read-only and always confirmed as a secret read (outside yolo).
func TestEnvDumpIsSecret(t *testing.T) {
	for _, c := range []string{"env", "printenv", "env | grep PATH", "printenv OPENAI_API_KEY", "export", "export -p", "set", "FOO=1 env", "ls; env"} {
		if SecretReason(c) == "" {
			t.Errorf("SecretReason(%q) empty: it dumps the environment", c)
		}
		if IsReadOnly(c) {
			t.Errorf("IsReadOnly(%q) = true", c)
		}
	}
	for _, c := range []string{"printenv HOME", "env FOO=1 make", "export PATH=$PATH:/x", "set -e", "env -u X go test ./..."} {
		if r := SecretReason(c); r != "" {
			t.Errorf("SecretReason(%q) = %q: it does not print keys", c, r)
		}
	}
}
