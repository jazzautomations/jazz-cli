package main

// mcp_presets.go: `jazz mcp` — known MCP servers the user can switch on with
// one command instead of writing mcp.json by hand. Only the user's global
// mcp.json is written (a project's .mcp.json is never touched).
//
//	jazz mcp presets                    list presets and whether they are on
//	jazz mcp enable context7 [--key K]  add/enable the preset (K: API key, optional)
//	jazz mcp disable context7           keep the entry, mark it disabled

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// mcpPreset is one known server.
type mcpPreset struct {
	Conf    MCPServerConf
	About   string // one line, pt-BR
	Privacy string // what leaves the machine, pt-BR
	KeyHdr  string // header that carries the optional key
}

var mcpPresets = map[string]mcpPreset{
	"context7": {
		Conf:    MCPServerConf{Type: "http", URL: dcContext7},
		About:   "Context7 (Upstash): documentação atualizada e por versão de bibliotecas; também vira a 1ª fonte remota da ferramenta docs",
		Privacy: "o nome da biblioteca e a consulta (query) vão para context7.com; não mande segredos ou código proprietário na query",
		KeyHdr:  "CONTEXT7_API_KEY",
	},
}

func mcpGlobalPath() string { return filepath.Join(configDir(), "mcp.json") }

// mcpReadRaw reads the global mcp.json keeping every field it has.
func mcpReadRaw() (map[string]json.RawMessage, map[string]MCPServerConf, error) {
	raw := map[string]json.RawMessage{}
	servers := map[string]MCPServerConf{}
	b, err := os.ReadFile(mcpGlobalPath())
	if err != nil {
		if os.IsNotExist(err) {
			return raw, servers, nil
		}
		return nil, nil, err
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, nil, fmt.Errorf("%s: JSON inválido: %w", mcpGlobalPath(), err)
	}
	if s, ok := raw["mcpServers"]; ok {
		if err := json.Unmarshal(s, &servers); err != nil {
			return nil, nil, fmt.Errorf("%s: mcpServers inválido: %w", mcpGlobalPath(), err)
		}
	}
	return raw, servers, nil
}

func mcpWriteRaw(raw map[string]json.RawMessage, servers map[string]MCPServerConf) error {
	sb, _ := json.Marshal(servers)
	raw["mcpServers"] = sb
	b, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(configDir(), 0o700); err != nil {
		return err
	}
	tmp := mcpGlobalPath() + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, mcpGlobalPath())
}

func cmdMCP(args []string, w io.Writer) int {
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "", "presets", "list", "ls":
		_, servers, err := mcpReadRaw()
		if err != nil {
			fmt.Fprintln(w, "jazz:", err)
			return 1
		}
		names := make([]string, 0, len(mcpPresets))
		for n := range mcpPresets {
			names = append(names, n)
		}
		sort.Strings(names)
		fmt.Fprintln(w, "presets MCP (jazz mcp enable <nome>):")
		for _, n := range names {
			st := "desligado"
			if c, ok := servers[n]; ok && !c.Disabled {
				st = "ligado"
			}
			fmt.Fprintf(w, "  %-10s %-9s %s\n", n, st, mcpPresets[n].About)
		}
		var other []string
		for n := range servers {
			if _, ok := mcpPresets[n]; !ok {
				other = append(other, n)
			}
		}
		sort.Strings(other)
		if len(other) > 0 {
			fmt.Fprintln(w, "outros servidores no mcp.json:", strings.Join(other, ", "))
		}
		return 0
	case "enable", "disable":
		if len(args) < 2 {
			fmt.Fprintf(w, "uso: jazz mcp %s <nome>\n", sub)
			return 2
		}
		name := strings.ToLower(args[1])
		raw, servers, err := mcpReadRaw()
		if err != nil {
			fmt.Fprintln(w, "jazz:", err)
			return 1
		}
		if sub == "disable" {
			c, ok := servers[name]
			if !ok {
				fmt.Fprintln(w, "jazz:", name, "não está no mcp.json")
				return 1
			}
			c.Disabled = true
			servers[name] = c
			if err := mcpWriteRaw(raw, servers); err != nil {
				fmt.Fprintln(w, "jazz:", err)
				return 1
			}
			fmt.Fprintln(w, name, "desligado em", mcpGlobalPath())
			return 0
		}
		p, ok := mcpPresets[name]
		if !ok {
			fmt.Fprintf(w, "jazz: preset desconhecido %q (jazz mcp presets lista os conhecidos)\n", name)
			return 2
		}
		c := p.Conf
		if old, ok := servers[name]; ok { // keep the user's own edits (headers, filters)
			c = old
			c.Disabled = false
		}
		for i := 2; i < len(args); i++ {
			a := args[i]
			key := ""
			switch {
			case (a == "--key" || a == "-key") && i+1 < len(args):
				key = args[i+1]
				i++
			case strings.HasPrefix(a, "--key="):
				key = strings.TrimPrefix(a, "--key=")
			default:
				fmt.Fprintf(w, "jazz: argumento desconhecido %q (uso: jazz mcp enable %s [--key CHAVE|--key '${VAR}'])\n", a, name)
				return 2
			}
			if p.KeyHdr != "" && key != "" {
				if c.Headers == nil {
					c.Headers = map[string]string{}
				}
				c.Headers[p.KeyHdr] = key
			}
		}
		servers[name] = c
		if err := mcpWriteRaw(raw, servers); err != nil {
			fmt.Fprintln(w, "jazz:", err)
			return 1
		}
		fmt.Fprintf(w, "%s ligado em %s\n", name, mcpGlobalPath())
		if p.Privacy != "" {
			fmt.Fprintln(w, "privacidade:", p.Privacy)
		}
		if name == "context7" {
			fmt.Fprintln(w, "a ferramenta docs passa a consultar o Context7 depois das fontes locais; desligue com: jazz mcp disable context7")
		}
		return 0
	}
	fmt.Fprintln(w, "uso: jazz mcp presets | jazz mcp enable <nome> [--key CHAVE] | jazz mcp disable <nome>")
	return 2
}
