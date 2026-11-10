package main

import (
	"os"
	"path/filepath"
	"strings"
)

// netlockout.go — o agente não serra o galho em que está sentado.
//
// Incidente real (2026-11-09): um agente com sudo, numa sessão SSH, rodou
// `sv down connman` na máquina de referência. O connman dava IP pro cabo e pro
// WiFi; minutos depois a máquina sumiu da rede inteira e só voltou com alguém
// no teclado. Nenhum comando ali era "destrutivo" no sentido clássico (nada
// apagado, nada formatado), então nenhuma guarda disparou.
//
// nkLockout reconhece o comando que derruba a rede ou o acesso remoto DESTA
// máquina: parar/desabilitar o gerenciador de rede, o sshd ou a VPN, baixar
// interface, bloquear rádio, política DROP no firewall, descarregar driver de
// rede. `restart` não entra: o serviço volta sozinho. A análise é heurística
// (como o resto de safety.go): cobre as formas comuns, não toda sintaxe.
//
// O veredito entra em segmentDanger, então vale como comando perigoso: pede
// confirmação em qualquer modo, e a mensagem diz como armar a volta antes.

// nkServices são os serviços cuja parada tira a máquina da rede ou fecha a
// porta por onde o dono entra. Nomes já em minúsculas, sem ".service".
var nkServices = map[string]bool{
	"connman": true, "connmand": true, "networkmanager": true, "network-manager": true,
	"networking": true, "network": true, "systemd-networkd": true, "netplan": true,
	"wpa_supplicant": true, "iwd": true, "dhcpcd": true, "dhclient": true, "dhcpcd5": true,
	"sshd": true, "ssh": true, "dropbear": true, "tailscaled": true, "tailscale": true,
	"wg-quick": true, "zerotier-one": true,
}

// nkStopVerbs por gerenciador de serviço.
var (
	nkSvStop        = map[string]bool{"down": true, "d": true, "stop": true, "force-stop": true, "force-shutdown": true, "kill": true, "k": true, "exit": true, "x": true, "shutdown": true}
	nkSystemctlStop = map[string]bool{"stop": true, "disable": true, "mask": true, "kill": true}
	nkServiceStop   = map[string]bool{"stop": true, "zap": true}
	nkModPrefixes   = []string{"iwl", "ipw", "ath", "rtl", "rtw", "brcm", "b43", "b44", "e1000", "igb", "igc", "ixgbe", "r8169", "r8152", "tg3", "bnx", "mt76", "mt79", "virtio_net", "cfg80211", "mac80211", "wl"}
)

func nkSvc(name string) bool {
	name = strings.ToLower(filepath.Base(name))
	name = strings.TrimSuffix(name, ".service")
	if i := strings.IndexByte(name, '@'); i > 0 { // wg-quick@wg0
		name = name[:i]
	}
	return nkServices[name]
}

// nkArgs drops option tokens (-x, --foo) so "systemctl --now disable sshd"
// and "systemctl disable --now sshd" read the same.
func nkArgs(args []string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			out = append(out, a)
		}
	}
	return out
}

func nkAnySvc(names []string) string {
	for _, n := range names {
		if nkSvc(n) {
			return strings.TrimSuffix(filepath.Base(n), ".service")
		}
	}
	return ""
}

// nkLockout returns what the simple command w args would take down, or "".
// w may be a path (/etc/init.d/networking).
func nkLockout(w string, args []string) string {
	full := w
	w = filepath.Base(w)
	a := nkArgs(args)
	low := func(i int) string {
		if i < len(a) {
			return strings.ToLower(a[i])
		}
		return ""
	}
	what := ""
	switch w {
	case "sv":
		if nkSvStop[low(0)] {
			what = nkAnySvc(a[1:])
		}
	case "systemctl":
		if nkSystemctlStop[low(0)] {
			what = nkAnySvc(a[1:])
		}
	case "service", "rc-service", "invoke-rc.d":
		if len(a) >= 2 && nkServiceStop[low(1)] && nkSvc(a[0]) {
			what = a[0]
		}
	case "rc-update", "update-rc.d":
		// rc-update del sshd · update-rc.d ssh disable|remove
		if len(a) >= 2 && (low(0) == "del" || low(0) == "delete") && nkSvc(a[1]) {
			what = a[1]
		} else if len(a) >= 2 && (low(1) == "disable" || low(1) == "remove") && nkSvc(a[0]) {
			what = a[0]
		}
	case "ifdown":
		for _, x := range a {
			if x != "lo" {
				what = "interface " + x
				break
			}
		}
	case "ifconfig":
		if len(a) >= 2 && a[0] != "lo" && low(1) == "down" {
			what = "interface " + a[0]
		}
	case "ip":
		// ip link set [dev] IF down
		if low(0) == "link" || low(0) == "l" {
			for i, x := range a {
				if strings.ToLower(x) == "down" && i >= 2 {
					ifc := a[i-1]
					if ifc != "lo" {
						what = "interface " + ifc
					}
				}
			}
		}
	case "nmcli":
		switch {
		case (low(0) == "networking" || low(0) == "n") && low(1) == "off":
			what = "toda a rede (nmcli networking off)"
		case (low(0) == "radio" || low(0) == "r") && len(a) >= 2 && low(len(a)-1) == "off":
			what = "rádio " + low(1)
		case (strings.HasPrefix("device", low(0)) && low(0) != "" && low(0)[0] == 'd') && (low(1) == "disconnect" || low(1) == "down"):
			what = "interface " + low(2)
		case (strings.HasPrefix("connection", low(0)) && low(0) != "" && low(0)[0] == 'c') && low(1) == "down":
			what = "conexão " + low(2)
		}
	case "connmanctl":
		if low(0) == "disable" && (low(1) == "wifi" || low(1) == "ethernet" || low(1) == "offline") || low(0) == "disconnect" {
			what = "connman " + low(0) + " " + low(1)
		} else if low(0) == "enable" && low(1) == "offline" {
			what = "modo offline do connman"
		}
	case "rfkill":
		if low(0) == "block" {
			what = "rádio " + low(1)
		}
	case "tailscale":
		if low(0) == "down" || low(0) == "logout" {
			what = "tailscale " + low(0)
		}
	case "wg-quick":
		if low(0) == "down" {
			what = "VPN " + low(1)
		}
	case "ufw":
		if low(0) == "enable" || low(0) == "default" && (low(1) == "deny" || low(1) == "reject") && low(2) != "outgoing" {
			what = "firewall (ufw " + strings.Join(a, " ") + ")"
		}
	case "iptables", "ip6tables", "iptables-nft", "iptables-legacy":
		// -P INPUT|OUTPUT DROP|REJECT: a política padrão passa a recusar tudo
		for i, x := range args {
			if (x == "-P" || x == "--policy") && i+2 < len(args) {
				ch, pol := strings.ToUpper(args[i+1]), strings.ToUpper(args[i+2])
				if (ch == "INPUT" || ch == "OUTPUT") && (pol == "DROP" || pol == "REJECT") {
					what = "firewall (política " + ch + " " + pol + ")"
				}
			}
		}
	case "pkill", "killall", "killall5":
		if n := nkAnySvc(a); n != "" {
			what = "processo " + n
		}
	case "modprobe", "rmmod":
		remove := w == "rmmod"
		for _, x := range args {
			if x == "-r" || x == "--remove" {
				remove = true
			}
		}
		if remove {
			for _, m := range a {
				m = strings.ToLower(m)
				for _, p := range nkModPrefixes {
					if strings.HasPrefix(m, p) {
						what = "driver de rede " + m
					}
				}
			}
		}
	}
	if what == "" && strings.Contains(full, "/init.d/") && nkSvc(w) && nkServiceStop[low(0)] {
		what = w
	}
	if what == "" {
		return ""
	}
	r := "derruba a rede ou o acesso remoto desta máquina (" + strings.TrimSpace(what) + ")"
	if nkRemote() {
		r += " — esta sessão é remota (SSH): se cair, só volta com alguém no teclado. Arme a volta antes, por exemplo `(sleep 120; <comando que restaura>) &`, ou use restart em vez de parar"
	}
	return r
}

// nkRemoteEnv is swapped by tests.
var nkRemoteEnv = os.Getenv

// nkRemote: jazz was started inside an SSH session.
func nkRemote() bool {
	return nkRemoteEnv("SSH_CONNECTION") != "" || nkRemoteEnv("SSH_CLIENT") != "" || nkRemoteEnv("SSH_TTY") != ""
}

// nkSudoInner returns the command that sudo/doas would run: its own options
// (and the values of -u/-g/-h/-p/-C/-D/-R/-T/-U) are skipped.
func nkSudoInner(args []string) []string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			return args[i+1:]
		case a == "-u" || a == "-g" || a == "-h" || a == "-p" || a == "-C" || a == "-D" || a == "-R" || a == "-T" || a == "-U":
			i++
		case strings.HasPrefix(a, "-"):
		case strings.Contains(a, "=") && !strings.HasPrefix(a, "/"): // VAR=valor antes do comando
		default:
			return args[i:]
		}
	}
	return nil
}
