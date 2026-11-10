package main

import (
	"strings"
	"testing"
)

// O agente não derruba a rede nem o acesso remoto da própria máquina sem
// confirmação. Cada linha de "derruba" é uma forma do incidente de 2026-11-09
// (sv down connman por SSH) ou um parente próximo.
func TestNetLockoutDanger(t *testing.T) {
	defer func(f func(string) string) { nkRemoteEnv = f }(nkRemoteEnv)
	nkRemoteEnv = func(string) string { return "" }

	derruba := []string{
		"sv down connman",
		"sudo sv down connman",
		"sudo -S sv down connman",
		"sv stop NetworkManager",
		"sv force-stop sshd",
		"systemctl stop NetworkManager",
		"sudo systemctl stop NetworkManager.service",
		"systemctl disable --now sshd",
		"systemctl --now disable ssh",
		"systemctl mask systemd-networkd",
		"systemctl stop wg-quick@wg0",
		"service networking stop",
		"rc-service connman stop",
		"/etc/init.d/networking stop",
		"rc-update del sshd default",
		"ifdown eth0",
		"ifconfig eth0 down",
		"ip link set eth0 down",
		"sudo ip link set dev wlan0 down",
		"nmcli networking off",
		"nmcli radio wifi off",
		"nmcli device disconnect wlan0",
		"nmcli con down Casa",
		"connmanctl disable wifi",
		"rfkill block all",
		"tailscale down",
		"sudo tailscale logout",
		"ufw enable",
		"ufw default deny incoming",
		"iptables -P INPUT DROP",
		"pkill connmand",
		"sudo killall sshd",
		"modprobe -r iwlwifi",
		"sudo rmmod ipw2200",
		"echo ok && sudo sv down connman",
		"ls; systemctl stop tailscaled",
	}
	for _, c := range derruba {
		r := DangerReason(c)
		if !strings.Contains(r, "derruba a rede") {
			t.Errorf("%q devia ser reconhecido como auto-bloqueio de rede, veio %q", c, r)
		}
		if strings.Contains(r, "sessão é remota") {
			t.Errorf("%q: sem SSH não deve falar de sessão remota: %q", c, r)
		}
	}

	// restart volta sozinho; leitura e serviços que não são de rede não entram.
	passa := []string{
		"sv status connman",
		"sv restart connman",
		"sv up connman",
		"sv down casca",
		"systemctl status NetworkManager",
		"systemctl restart NetworkManager",
		"systemctl stop nginx",
		"systemctl disable bluetooth",
		"service nginx stop",
		"ifdown lo",
		"ip link show",
		"ip link set eth0 up",
		"ip addr show eth0",
		"nmcli device status",
		"nmcli radio",
		"connmanctl technologies",
		"rfkill list",
		"tailscale status",
		"tailscale up",
		"ufw status",
		"ufw default deny outgoing",
		"iptables -L",
		"iptables -P FORWARD DROP",
		"pkill firefox",
		"modprobe iwlwifi",
		"modprobe -r pcspkr",
		"grep -r connman /etc",
		"echo sv down connman",
	}
	for _, c := range passa {
		if r := DangerReason(c); strings.Contains(r, "derruba a rede") {
			t.Errorf("%q NÃO devia ser auto-bloqueio de rede: %q", c, r)
		}
	}
}

// Em sessão SSH a mensagem diz o que está em jogo e como armar a volta.
func TestNetLockoutRemoteSessionMessage(t *testing.T) {
	defer func(f func(string) string) { nkRemoteEnv = f }(nkRemoteEnv)
	nkRemoteEnv = func(k string) string {
		if k == "SSH_CONNECTION" {
			return "192.0.2.10 50000 192.0.2.20 22"
		}
		return ""
	}
	r := DangerReason("sudo sv down connman")
	for _, want := range []string{"derruba a rede", "connman", "sessão é remota", "sleep 120"} {
		if !strings.Contains(r, want) {
			t.Errorf("mensagem em sessão remota sem %q: %q", want, r)
		}
	}
}

func TestNkSudoInner(t *testing.T) {
	for in, want := range map[string]string{
		"-S sv down connman":            "sv down connman",
		"-u root -n systemctl stop ssh": "systemctl stop ssh",
		"-- ifdown eth0":                "ifdown eth0",
		"FOO=1 tailscale down":          "tailscale down",
		"-S":                            "",
	} {
		if got := strings.Join(nkSudoInner(strings.Fields(in)), " "); got != want {
			t.Errorf("nkSudoInner(%q) = %q, quero %q", in, got, want)
		}
	}
}
