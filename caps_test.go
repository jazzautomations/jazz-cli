package main

import (
	"context"
	"strings"
	"testing"
)

// Menor intervenção: what a project task never needs to write.
func TestCapsSystemCritical(t *testing.T) {
	crit := []string{
		"sudo tee /etc/containers/registries.conf",
		"echo 'unqualified-search-registries = [\"docker.io\"]' > /etc/containers/registries.conf",
		"sudo sed -i 's/cgroup_manager.*/cgroup_manager = \"cgroupfs\"/' /usr/share/containers/containers.conf /etc/containers/containers.conf",
		"rm -rf ~/.config/containers",
		"mv ~/.local/share/containers /tmp/x",
		"sudo modprobe overlay",
		"sudo sysctl -w net.ipv4.ip_forward=1",
		"wsl --shutdown",
		"wsl.exe --unregister Ubuntu",
		"podman system reset -f",
		"podman machine rm -f",
		"sudo systemctl restart docker",
		"sudo cp mybin /usr/local/bin/",
		"sudo chmod 777 /usr/bin/newuidmap",
		"echo 1 | sudo tee /proc/sys/kernel/unprivileged_userns_clone",
		"nano /etc/wsl.conf",
		"printf '[wsl2]\\nmemory=4GB' > $HOME/.wslconfig",
		"sudo update-grub",
	}
	for _, c := range crit {
		if why := cbSystemCritical("bash", nil, c); why == "" {
			t.Errorf("not critical: %s", c)
		}
	}
	safe := []string{
		"cat /etc/os-release",
		"ls /usr/bin | head",
		"podman ps",
		"podman run --rm -p 8080:80 nginx",
		"docker compose up -d",
		"go test ./...",
		"cp /etc/hosts ./hosts.bak",
		"systemctl status docker",
		"rm -rf ./build",
		"grep -r cgroup /etc/containers",
		"echo hi > /dev/null",
		"make 2>&1 | tail",
	}
	for _, c := range safe {
		if why := cbSystemCritical("bash", nil, c); why != "" {
			t.Errorf("critical by mistake: %s (%s)", c, why)
		}
	}
	if why := cbSystemCritical("write", map[string]any{"path": "/etc/containers/policy.json"}, ""); why == "" {
		t.Error("write to /etc must be critical")
	}
	if why := cbSystemCritical("edit", map[string]any{"path": "src/main.go"}, ""); why != "" {
		t.Error("a project edit is not critical")
	}
}

// A container task that wants to remove or reset the engine is blocked as
// disproportionate; the same command outside a container task is not ours.
func TestCapsDisproportionateForContainerTask(t *testing.T) {
	goal := "sobe o container do postgres com podman"
	for _, c := range []string{"sudo apt-get remove -y podman", "sudo apt purge docker-ce", "podman system prune -a -f", "rm -rf ~/.local/share/containers/storage"} {
		if why := cbMinimalIntervention("bash", nil, c, goal); why == "" {
			t.Errorf("not blocked for a container goal: %s", c)
		}
	}
	if why := cbDisproportionate("atualiza as dependências", "sudo apt-get remove -y podman"); why != "" {
		t.Errorf("not a container task: %s", why)
	}
	why := cbMinimalIntervention("bash", nil, "sudo tee /etc/containers/registries.conf", goal)
	if !strings.Contains(why, "desproporcional") {
		t.Errorf("the explanation names the proportion: %s", why)
	}
}

// CRITICAL in every mode: yolo and turbo cannot auto-approve it.
func TestCapsCriticalBlocksInYoloAndTurbo(t *testing.T) {
	for _, mode := range []string{"yolo", "turbo", "auto"} {
		h := newHarness(t, nil, func(c *Config) { c.Mode = mode })
		h.ui.answer = Allow
		ok, why, _ := h.ag.permit(context.Background(), ToolCall{Name: "bash", Args: jsonArgs(map[string]any{"command": "sudo tee /etc/containers/registries.conf"})}, "", false, acHookVerdict{})
		if ok {
			t.Fatalf("%s: a write to /etc/containers ran", mode)
		}
		if !strings.Contains(why, "menor intervenção") && !strings.Contains(why, "CRÍTICO") {
			t.Errorf("%s: reason %q", mode, why)
		}
		ok, _, _ = h.ag.permit(context.Background(), ToolCall{Name: "bash", Args: jsonArgs(map[string]any{"command": "cat /etc/os-release"})}, "", false, acHookVerdict{})
		if !ok {
			t.Errorf("%s: reading /etc is fine", mode)
		}
	}
}

func TestCapsRegistry(t *testing.T) {
	tb := NewToolbox(t.TempDir())
	defer tb.Shell.Close()
	reg := cbRegistry(tb)
	for _, n := range []string{"resolve_name", "read_file", "run_tests", "git_status", "env_status", "read", "bash", "script:git_state"} {
		if _, ok := cbFind(reg, n); !ok && (n != "script:git_state" || tb.scRegistry().python != "") {
			t.Errorf("capability %s missing", n)
		}
	}
	for _, c := range reg {
		if c.Name == "" || c.Kind == "" {
			t.Errorf("incomplete capability %+v", c)
		}
	}
	offline := &epProfile{Online: false, PathBins: []string{"ls"}}
	if ok, _ := cbAvailable(cbToolCaps["fetch"], offline); ok {
		t.Error("fetch needs the network")
	}
	gs, _ := cbFind(reg, "git_status")
	if ok, why := cbAvailable(gs, offline); ok || !strings.Contains(why, "git") {
		t.Errorf("git_status without git: %v %s", ok, why)
	}
}

// Never send what the model does not support: the user gets a
// deterministic message that names a model that does.
func TestCapsModelVision(t *testing.T) {
	no, yes := false, true
	cfg := &Config{Providers: map[string]*ProviderConf{
		"a": {Type: "openai", DefaultModel: "texto", NoKey: true},
		"b": {Type: "openai", DefaultModel: "olho", NoKey: true},
	}, Models: map[string]*ModelConf{"a/texto": {Images: &no}, "b/olho": {Images: &yes}}}
	if c := cbModelOf(cfg, "a/texto"); c.Vision || !c.VisionKnown {
		t.Fatalf("a/texto: %+v", c)
	}
	msg := cbUnsupported(cfg, "a/texto", cbNeed{Vision: true})
	if !strings.Contains(msg, "não aceita imagem") || !strings.Contains(msg, "b/olho") {
		t.Fatalf("message: %q", msg)
	}
	if msg := cbUnsupported(cfg, "b/olho", cbNeed{Vision: true}); msg != "" {
		t.Fatalf("a vision model: %q", msg)
	}
	// stripping keeps the image out of the request
	out := acStripImages([]Message{{Role: "user", Content: "x", Images: []ImagePart{{}}}}, "a/texto")
	if len(out[0].Images) != 0 {
		t.Fatal("image sent to a model without vision")
	}
}
