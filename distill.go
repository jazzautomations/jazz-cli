package main

// distill.go — a ponte online→offline (0.15 "destilação").
//
// O que já existia: o plancache (plancache.go) grava um plano de cada corrida
// do MODELO com sucesso verificado, e o reusa como dica quando o modelo volta.
// Isso alimenta o modelo — nunca o cérebro offline. Esta é a peça que faltava:
//
//	plano verificado E REPETIDO (Wins ≥ dsMinWins) → método offline aprendido
//
// Um plano é destilável quando toda a trajetória que verificou são comandos
// bash (pcPlan.Cmds, os literais por trás dos Steps clipados) — o que o
// modelo fez de forma determinística, o cérebro repete de graça. A
// DECOMPOSIÇÃO é o próprio método: a tarefa vira a sequência de sub-passos
// verificáveis que resolveu ela, replayável com zero tokens.
//
// Com slots: variantes do mesmo Shape ("comprime docs" e "comprime src" →
// "comprime @dir") viram TESTEMUNHAS de um único plano (pcPlan.Witnesses),
// e a anti-unificação prova parâmetros — o valor da entidade é o que muda
// de uma corrida pra outra; preencher o template com os valores de cada
// testemunha reconstrói o comando dela byte a byte. Prova dada, o método
// sai parametrizado ($slot.x tipado + pré-condição + atalho mascarado);
// sem prova, fica literal como sempre.
//
// Segurança, em camadas (nenhuma contorna a outra):
//
//   - destilação: todo comando passa dsCmdOK (sem DangerReason, sem
//     SecretReason, uma linha, tamanho) E dsReplaySafe (só-leitura ou
//     ferramenta de build/teste conhecida) — comando que muta estado
//     compartilhado nunca vira método, porque o portão não conseguiria
//     re-verificá-lo sem mutar de verdade.
//   - portão (`jazz distill`): (a) REPLAY — a sequência roda de novo no
//     projeto e TODOS os passos saem exit 0, senão rejeita; (b) quando a
//     candidata cria uma intenção aprendida nova, o mesmo portão held-out
//     da forja (NonInferior + âncora de release + McNemar por intenção +
//     guarda estrita) prova que a gramática não regride, e o teste dirigido
//     prova que o texto do plano lê na intenção nova.
//   - carga: o overlay só aceita passos learned_cmd e checks
//     exit_zero/cmd_ran — nem escrito à mão ele carrega outra capacidade.
//   - execução: learned_cmd roda pelo mesmo env.Permit de qualquer comando
//     do cérebro; intenções mutantes (fgIsMutating) nunca recebem método
//     nem atalho aprendido.
//
// Arquivo: dataDir()/forge/methods.overlay.json — {version, intents,
// methods, aliases, prov}. Mesma disciplina do overlay da gramática: some
// com JAZZ_FORGE=off, --fresh, eval offline e binários de teste.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	dsOverlayVersion = 1
	dsOverlayMaxByte = 1 << 20
	dsMinWins        = 2 // um procedimento aprende-se quando verificou e se repetiu
	dsMaxSteps       = 8
	// dsCapGen: a geração das caps destiláveis — sobe quando uma família
	// nova entra (copy/link/to_dir etc) e reabre os carimbos antigos.
	dsCapGen        = 5
	dsMaxCmd        = 240
	dsMaxCand       = 4   // orçamento do portão por rodada
	dsMaxMethods    = 100 // teto do overlay inteiro
	dsMaxIntents    = 50
	dsLearnedPrefix = "learned_"
	dsCmdTimeout    = 10 * time.Minute // como proj_cmd: um passo de build/teste pode demorar
)

// dsIntent é uma intenção aprendida: nome learned_*, sem regras, só
// exemplos — o classificador a vê (paráfrases) e o atalho a acerta (o mesmo
// texto de novo = replay exato). Slots, quando o plano provou parâmetros:
// o atalho mascarado preenche cada um com a entidade do tipo certo.
type dsIntent struct {
	Name     string               `json:"name"`
	Desc     string               `json:"desc,omitempty"`
	Examples []string             `json:"examples"`
	Slots    map[string]nuSlotDef `json:"slots,omitempty"`
}

// dsProv é a proveniência de um método aprendido: de qual plano ele veio.
type dsProv struct {
	Plan   string `json:"plan"`
	Task   string `json:"task"`
	Intent string `json:"intent"`
	Method string `json:"method"`
	Wins   int    `json:"wins"`
	Ts     int64  `json:"ts"`
}

// dsOverlay é o acervo de métodos aprendidos.
type dsOverlay struct {
	Version int                `json:"version"`
	Intents []dsIntent         `json:"intents,omitempty"`
	Methods []mhMethod         `json:"methods,omitempty"`
	Aliases map[string]nuAlias `json:"aliases,omitempty"` // canon -> atalho exato
	Prov    []dsProv           `json:"prov,omitempty"`
}

// dsOverlayPath é uma var para os testes apontarem o arquivo a um TempDir.
var dsOverlayPath = func() string {
	return filepath.Join(dataDir(), "forge", "methods.overlay.json")
}

// dsReadOverlay lê e valida o envelope; arquivo ausente é (nil, nil).
func dsReadOverlay(path string) (*dsOverlay, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, dsOverlayMaxByte+1))
	if err != nil {
		return nil, err
	}
	if len(b) > dsOverlayMaxByte {
		return nil, errors.New("overlay de métodos maior que 1 MiB")
	}
	var ov dsOverlay
	if err := json.Unmarshal(b, &ov); err != nil {
		return nil, err
	}
	if ov.Version != dsOverlayVersion {
		return nil, errors.New("versão do overlay de métodos desconhecida")
	}
	return &ov, nil
}

// dsWriteOverlay grava atomicamente (tmp + rename, 0600).
func dsWriteOverlay(path string, ov *dsOverlay) error {
	ov.Version = dsOverlayVersion
	b, err := json.MarshalIndent(ov, "", " ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// ---------- validação de comandos e passos ----------

// dsCmdOK é a barra de TODO comando aprendido, na carga e na execução: sem
// perigo (DangerReason — destructivos, forks, escritas fora), sem leitura de
// segredo (SecretReason), uma linha só, tamanho contido. "" = permitido.
func dsCmdOK(cmd string) string {
	c := strings.TrimSpace(cmd)
	switch {
	case c == "":
		return "vazio"
	case len(c) > dsMaxCmd:
		return "comando longo demais"
	case strings.ContainsAny(c, "\n\r\x00"):
		return "multi-linha ou byte de controle"
	}
	if r := DangerReason(c); r != "" {
		return "perigoso: " + r
	}
	if r := SecretReason(c); r != "" {
		return "toca segredo: " + r
	}
	return dsSecretFields(c)
}

// dsSecretFields pega o que escapa do SecretReason: um segredo atrás de uma
// REVISÃO — `git show HEAD:.env`, `git cat-file blob main:.npmrc`,
// `tar -Ox pacote.tar:id_rsa` — cujo basename não é o do arquivo.
func dsSecretFields(c string) string {
	for _, f := range strings.Fields(c) {
		i := strings.IndexByte(f, ':')
		if i <= 0 {
			continue
		}
		suf := strings.Trim(f[i+1:], `"'`)
		if suf == "" || strings.ContainsAny(suf, "*?[") {
			continue
		}
		if reSecretArg.MatchString(suf) || secretName(filepath.Base(suf)) {
			return "toca segredo: " + f
		}
	}
	return ""
}

// dsReplayCmds: ferramentas de build/teste/diagnóstico cujo replay no
// portão não muta estado compartilhado (no máximo artefatos do próprio
// projeto, que a corrida verificada já produziu). O regex casa o resto da
// linha depois do nome do binário. Tudo fora da lista — git mutante, ssh,
// curl, rm, apt, pip install, docker exec, systemctl — não é reproduzível.
var dsReplayCmds = map[string]*lazyRe{
	"go":            lazyRx(`^(test|build|vet|fmt|list|doc|env|version|mod\s+(tidy|verify|download|graph|why|vendor))(\s|$)`),
	"gofmt":         lazyRx(`^(-[a-z]+\s+)*\S`),
	"gofumpt":       lazyRx(`^(-[a-z]+\s+)*\S`),
	"goimports":     lazyRx(`^(-[a-z]+\s+)*\S`),
	"golangci-lint": lazyRx(`^(run|linters|version|config)(\s|$)`),
	"staticcheck":   lazyRx(`^(\S|$)`),
	"cargo":         lazyRx(`^(test|build|check|clippy|fmt|doc|tree|metadata)(\s|$)`),
	"rustc":         lazyRx(`^--version|(\s|$)`),
	"rustfmt":       lazyRx(`^(\s|$)`),
	"npm":           lazyRx(`^(test|t|ls|outdated|audit|view|pack|exec|run\s+(test|build|lint|check|verify|coverage|compile|tsc|typecheck|type-check))(\s|$)`),
	"yarn":          lazyRx(`^(test|t|run\s+(test|build|lint|check)|build|lint|check|tsc|why|outdated|list|info)(\s|$)`),
	"pnpm":          lazyRx(`^(test|t|run\s+(test|build|lint|check)|build|lint|check|why|outdated|list|exec\s+(tsc|eslint|jest|vitest))(\s|$)`),
	"npx":           lazyRx(`^(tsc|eslint|jest|vitest|prettier|mocha|tsx?|node)(\s|@|$)`),
	"node":          lazyRx(`^(--version|-v|-e\s+['"]?console|\S+\.(mjs|cjs|js))(\s|$)`),
	"deno":          lazyRx(`^(test|check|lint|fmt|eval|info|doc|coverage)(\s|$)`),
	"tsc":           lazyRx(`^(\s|$)`),
	"eslint":        lazyRx(`^(\s|$)`),
	"prettier":      lazyRx(`^(\s|$)`),
	"jest":          lazyRx(`^(\s|$)`),
	"vitest":        lazyRx(`^(\s|$)`),
	"mocha":         lazyRx(`^(\s|$)`),
	"pytest":        lazyRx(`^(\s|$)`),
	"py.test":       lazyRx(`^(\s|$)`),
	"python":        lazyRx(`^(-c\s+['"]?import\s+\w+|[^\s]*\.py|-m\s+(pytest|unittest|mypy|ruff|compileall|coverage|venv|pip\s+(list|show|check|freeze)|build|twine\s+check))(\s|$)`),
	"python3":       lazyRx(`^(-c\s+['"]?import\s+\w+|[^\s]*\.py|-m\s+(pytest|unittest|mypy|ruff|compileall|coverage|venv|pip\s+(list|show|check|freeze)|build|twine\s+check))(\s|$)`),
	"ruff":          lazyRx(`^(check|format|rule|linter|version|clean)(\s|$)`),
	"mypy":          lazyRx(`^(\s|$)`),
	"black":         lazyRx(`^(\s|$)`),
	"isort":         lazyRx(`^(\s|$)`),
	"flake8":        lazyRx(`^(\s|$)`),
	"pylint":        lazyRx(`^(\s|$)`),
	"shellcheck":    lazyRx(`^(\s|$)`),
	"hadolint":      lazyRx(`^(\s|$)`),
	"shfmt":         lazyRx(`^(\s|$)`),
	"make":          lazyRx(`^($|(-\w+\s+)*(all|test|check|build|lint|vet|fmt|compile|unit|coverage|verify|ci|bench|deps)\s*$)`),
	"cmake":         lazyRx(`^(--build|--version|-S\s|--list-presets)(\s|$)`),
	"ctest":         lazyRx(`^(\s|$)`),
	"ninja":         lazyRx(`^(\s|$)`),
	"meson":         lazyRx(`^(test|compile|setup|introspect|info)(\s|$)`),
	"bazel":         lazyRx(`^(test|build|query|info|check)(\s|$)`),
	"mvn":           lazyRx(`^(test|verify|compile|package|dependency:|help:|validate)(\s|$)`),
	"mvnw":          lazyRx(`^(test|verify|compile|package|dependency:|help:|validate)(\s|$)`),
	"gradle":        lazyRx(`^(test|build|check|tasks|dependencies|compileJava|compileKotlin)(\s|$)`),
	"gradlew":       lazyRx(`^(test|build|check|tasks|dependencies|compileJava|compileKotlin)(\s|$)`),
	"javac":         lazyRx(`^(\s|$)`),
	"dotnet":        lazyRx(`^(test|build|restore|list|clean|format|vet|sln|package)(\s|$)`),
	"swift":         lazyRx(`^(test|build|package|describe|diagnose)(\s|$)`),
	"mix":           lazyRx(`^(test|compile|deps\.|format|dialyzer|credo|spec)(\s|$)`),
	"rebar3":        lazyRx(`^(eunit|ct|compile|dialyzer|as)(\s|$)`),
	"zig":           lazyRx(`^(test|build|ast-check|fmt|version)(\s|$)`),
	"bundle":        lazyRx(`^(exec\s+(rspec|rake|rubocop)|check|list|outdated|info|show)(\s|$)`),
	"rake":          lazyRx(`^(test|spec|default|check|-T)(\s|$)`),
	"rspec":         lazyRx(`^(\s|$)`),
	"rubocop":       lazyRx(`^(\s|$)`),
	"phpunit":       lazyRx(`^(\s|$)`),
	"composer":      lazyRx(`^(test|check|validate|install|update|dump-autoload|cs-fix|analyse|run-script\s+\w*(test|check|lint|analy))(\s|$)`),
	"kotlinc":       lazyRx(`^(\s|$)`),
	"sbt":           lazyRx(`^(test|compile|check|clean|about|inspect)(\s|$)`),
	"flutter":       lazyRx(`^(test|analyze|doctor|pub get|build)(\s|$)`),
	"dart":          lazyRx(`^(test|analyze|format|pub get|run\s+\S+\.dart|compile)(\s|$)`),
	"buf":           lazyRx(`^(lint|build|generate|format|breaking|ls-files)(\s|$)`),
	"protoc":        lazyRx(`^(\s|$)`),
	"jq":            lazyRx(`^(\s|$)`),
	"yq":            lazyRx(`^(\s|$)`),
}

// dsReplaySafe: pode o portão rodar este comando de novo sem mutar nada que
// a corrida original não tenha mutado? Só-leitura direto; senão, cada
// segmento precisa ser um binário da lista de build/teste com subcomando
// permitido.
func dsReplaySafe(cmd string) bool {
	c := strings.TrimSpace(cmd)
	if c == "" {
		return false
	}
	if IsReadOnly(c) {
		return true
	}
	for _, part := range reSplit.Split(c, -1) {
		p := strings.TrimSpace(part)
		if p == "" {
			continue
		}
		if IsReadOnly(p) {
			continue
		}
		toks := shellFields(p)
		if len(toks) == 0 {
			return false
		}
		re, ok := dsReplayCmds[filepath.Base(toks[0])]
		if !ok {
			return false
		}
		rest := strings.TrimSpace(strings.TrimPrefix(p, toks[0]))
		if !re.MatchString(rest) {
			return false
		}
	}
	return true
}

// dsIntentNameOK: intenção aprendida é learned_ seguido de slug minúsculo.
var dsIntentNameRe = lazyRx(`^learned_[a-z0-9_]{1,48}$`)

func dsIntentNameOK(name string) bool { return dsIntentNameRe.MatchString(name) }

// dsValidMethod valida um método do overlay na carga — editado à mão ou
// não, a mesma barra: passos learned_cmd dentro do reproduzível, ou passos
// de capacidade espelhando os shapes que o destilador emite (dsCapStepOK);
// checks exit_zero/cmd_ran sempre, e os checks de filesystem/script só num
// método de caps; intenção conhecida (gramática ou aprendida), nunca
// mutante, nada de registro de comandos embutidos (Cmds é do acervo).
func dsValidMethod(m mhMethod, intentOK func(string) bool) string {
	if m.Name == "" || m.Intent == "" || strings.ContainsAny(m.Name+m.Intent, " \t\n") {
		return "nome ou intenção inválido"
	}
	if !intentOK(m.Intent) {
		return "intenção desconhecida: " + m.Intent
	}
	if fgIsMutating(m.Intent) {
		return "intenção mutante: método aprendido não se liga a ela"
	}
	if len(m.Cmds) > 0 {
		return "o overlay não declara comandos de registro (cmds)"
	}
	if len(m.Steps) == 0 || len(m.Steps) > dsMaxSteps {
		return "número de passos fora do permitido"
	}
	outs := map[string]bool{}
	lists := map[string]bool{} // outs de produtores de lista (script:*)
	capM := false
	for _, st := range m.Steps {
		if st.Cap != "learned_cmd" {
			capM = true
			break
		}
	}
	for i, st := range m.Steps {
		if st.Cap == "learned_cmd" {
			cmd := st.In["cmd"]
			eff := cmd
			if refs := dsSlotNames(cmd); len(refs) > 0 {
				// template parametrizado: cada placeholder precisa estar
				// declarado e guardado por pré-condição, nunca pode ser a
				// palavra de comando, e a substituição (aqui uma sonda "x")
				// continua dentro da barra de replay.
				if dsSlotInCmdWord(cmd) {
					return fmt.Sprintf("passo %d: slot na palavra de comando", i+1)
				}
				for _, n := range refs {
					if _, ok := m.Slots[n]; !ok {
						return fmt.Sprintf("passo %d: $slot.%s não declarado em slots", i+1, n)
					}
					if !dsPreHas(m.Pre, "slot."+n) {
						return fmt.Sprintf("passo %d: $slot.%s sem pré-condição slot.%s set", i+1, n, n)
					}
					eff = strings.ReplaceAll(eff, "$slot."+n, "x")
				}
			}
			if why := dsCmdOK(eff); why != "" {
				return fmt.Sprintf("passo %d: %s", i+1, why)
			}
			// literal ou template já expandido pela sonda: fora do replay-safe
			// não carrega — um overlay adulterado não vira bypass da cerca.
			if !dsReplaySafe(eff) {
				return fmt.Sprintf("passo %d: comando fora do reproduzível", i+1)
			}
			for k := range st.In {
				if k != "cmd" {
					return fmt.Sprintf("passo %d: entrada desconhecida %q", i+1, k)
				}
			}
		} else if why := dsCapStepOK(st, outs, lists); why != "" {
			return fmt.Sprintf("passo %d: %s", i+1, why)
		}
		if st.Out != "" {
			outs[st.Out] = true
			if strings.HasPrefix(st.Cap, "script:") {
				lists[st.Out] = true
			}
		}
	}
	if len(m.Verify) == 0 {
		return "método aprendido sem verificação"
	}
	for _, v := range m.Verify {
		if v.Check != "exit_zero" && v.Check != "cmd_ran" && !(capM && dsCapChecks[v.Check]) {
			return "verificador não permitido: " + v.Check
		}
		arg, _ := strings.CutPrefix(v.Arg, "$")
		if !outs[arg] {
			return "verificador aponta um passo inexistente: " + v.Arg
		}
		if v.Arg2 != "" && v.Check != "list_present" &&
			v.Check != "file_contains" && v.Check != "file_lacks" && v.Check != "list_lacks" &&
			v.Check != "file_mode" && v.Check != "list_mode" &&
			v.Check != "file_owner" && v.Check != "file_group" &&
			v.Check != "list_owner" && v.Check != "list_group" &&
			v.Check != "file_perm" && v.Check != "file_noperm" && v.Check != "list_perm" {
			return "verificador com arg2 inesperado: " + v.Check
		}
		if v.Check == "list_present" && !dsPathLit(v.Arg2) {
			return "verificador list_present: dir não literal"
		}
		if (v.Check == "file_contains" || v.Check == "file_lacks" || v.Check == "list_lacks") &&
			strings.ContainsAny(v.Arg2, "\x00\n\r") {
			return "verificador " + v.Check + ": agulha com byte de controle"
		}
	}
	for _, c := range m.Pre {
		if c.Fact == "" || (c.Op != "" && c.Op != "set" && c.Op != "unset" && c.Op != "eq" && c.Op != "ne") {
			return "pré-condição mal formada"
		}
	}
	for n, k := range m.Slots {
		if !dsSlotNameRe.MatchString(n) || !dsKindOK(k) {
			return "slot mal declarado: " + n
		}
	}
	return ""
}

// dsCapKeys: as únicas capacidades que um método destilado pode usar e as
// entradas conhecidas de cada uma — espelhadas dos métodos reais
// (delete_file.each: file_find produz, for_each ramifica pra file_op;
// rename.op/create.file: file_op age confinado no projeto).
var dsCapKeys = map[string]map[string]bool{
	"script:file_find": smSet("path", "name", "name_re", "ext", "type", "sort",
		"min_size", "max_size", "newer_min", "older_min", "empty", "exclude",
		"include_hidden", "time_field", "min_depth", "max_depth", "limit"),
	"script:perm_find": smSet("path", "type", "limit", "include_hidden",
		"mode", "not_mode", "world_writable", "not_world_writable",
		"suid", "sgid", "executable", "not_executable", "owner",
		"not_owner", "group", "not_group", "no_owner", "no_group", "orphan"),
	"for_each": smSet("from", "do", "op", "max", "to", "to_dir", "content",
		"from_str", "to_str", "ignore_case", "first_per_line", "backup_ext",
		"mode", "owner", "group", "recursive"),
	"save_list": smSet("from", "path", "sort"),
	"file_op":   smSet("op", "path", "to", "to_dir", "content"),
	"text_op": smSet("op", "path", "to", "from_str", "to_str",
		"ignore_case", "first_per_line", "backup_ext"),
	"archive_op": smSet("op", "out", "srcs", "arc", "dir"),
	"perm_op":    smSet("op", "path", "mode", "owner", "group", "recursive"),
}

// dsPermOps são os ops que o brPermOp conhece.
var dsPermOps = smSet("chmod", "chown", "chgrp")

// dsFileOpOps são os ops que o brFileOp conhece — os únicos que uma cap
// destilada pode pedir (direto ou via for_each).
var dsFileOpOps = smSet("create", "write", "append", "mkdir", "delete", "rename",
	"copy", "link", "symlink")

// dsCapChecks: verificadores extra que um método de caps pode usar — todos
// só-leitura e provam efeito em filesystem ou no resultado do script
// (brVerify); num método só de learned_cmd seguem valendo só
// exit_zero/cmd_ran.
var dsCapChecks = smSet("file_exists", "file_gone", "list_gone", "list_present",
	"list_exists",
	"file_mode", "file_owner", "file_group", "list_mode",
	"file_perm", "file_noperm", "list_perm", "list_owner", "list_group",
	"file_contains", "file_lacks", "list_lacks", "script_ok", "script_exit_zero")

// dsCapStepOK valida um passo de capacidade de método aprendido: cap da
// lista, chaves conhecidas, valores literais ou $out produzido antes
// (nunca $slot — método de caps não tem slot de texto), e o shape
// específico de cada cap (for_each consome uma lista produzida por
// script:* e só ramifica file_op; file_op exige op real e path).
func dsCapStepOK(st mhStep, outs, lists map[string]bool) string {
	keys, ok := dsCapKeys[st.Cap]
	if !ok {
		return "capacidade não destilável: " + st.Cap
	}
	for k, v := range st.In {
		if !keys[k] {
			return fmt.Sprintf("capacidade %s: entrada desconhecida %q", st.Cap, k)
		}
		if strings.Contains(v, "$slot.") {
			return "capacidade " + st.Cap + ": método de caps não usa $slot"
		}
		if strings.ContainsAny(v, "\n\r\x00") {
			return "capacidade " + st.Cap + ": valor com byte de controle"
		}
		if strings.HasPrefix(v, "$") && !outs[strings.TrimPrefix(v, "$")] {
			return fmt.Sprintf("capacidade %s: %s aponta um passo inexistente", st.Cap, k)
		}
	}
	switch st.Cap {
	case "for_each":
		if !lists[strings.TrimPrefix(st.In["from"], "$")] {
			return "capacidade for_each: from não é uma lista produzida por script"
		}
		switch st.In["do"] {
		case "file_op":
			if !dsFileOpOps[st.In["op"]] {
				return "capacidade for_each: op desconhecido " + st.In["op"]
			}
			switch st.In["op"] {
			case "rename", "copy", "link", "symlink":
				if strings.TrimSpace(st.In["to"]) == "" && strings.TrimSpace(st.In["to_dir"]) == "" {
					return "capacidade for_each: " + st.In["op"] + " sem to/to_dir"
				}
			}
		case "text_op":
			if st.In["op"] != "replace" {
				return "capacidade for_each: text_op só ramifica replace"
			}
			if st.In["from_str"] == "" {
				return "capacidade for_each: replace sem from_str"
			}
		case "perm_op":
			if err := dsPermShape(st.In, false); err != "" {
				return "capacidade for_each: " + err
			}
		default:
			return "capacidade for_each: do tem que ser file_op, text_op ou perm_op"
		}
	case "save_list":
		if !lists[strings.TrimPrefix(st.In["from"], "$")] {
			return "capacidade save_list: from não é uma lista produzida por script"
		}
		if strings.TrimSpace(st.In["path"]) == "" {
			return "capacidade save_list: falta path"
		}
	case "file_op":
		if !dsFileOpOps[st.In["op"]] {
			return "capacidade file_op: op desconhecido " + st.In["op"]
		}
		if strings.TrimSpace(st.In["path"]) == "" {
			return "capacidade file_op: falta path"
		}
		switch st.In["op"] {
		case "rename":
			if strings.TrimSpace(st.In["to"]) == "" && strings.TrimSpace(st.In["to_dir"]) == "" {
				return "capacidade file_op: rename sem to/to_dir"
			}
		case "copy", "link", "symlink":
			if strings.TrimSpace(st.In["to"]) == "" && strings.TrimSpace(st.In["to_dir"]) == "" {
				return "capacidade file_op: " + st.In["op"] + " sem to/to_dir"
			}
		}
	case "text_op":
		if st.In["op"] != "replace" {
			return "capacidade text_op: op desconhecido " + st.In["op"]
		}
		if strings.TrimSpace(st.In["path"]) == "" {
			return "capacidade text_op: falta path"
		}
		if st.In["from_str"] == "" {
			return "capacidade text_op: replace sem from_str"
		}
		if st.In["to"] != "" && !dsPathLit(st.In["to"]) {
			return "capacidade text_op: destino não literal"
		}
	case "perm_op":
		if err := dsPermShape(st.In, true); err != "" {
			return "capacidade perm_op: " + err
		}
	case "archive_op":
		switch st.In["op"] {
		case "create":
			if !dsPathLit(st.In["out"]) || strings.TrimSpace(st.In["out"]) == "" {
				return "capacidade archive_op: create sem out literal"
			}
			if strings.TrimSpace(st.In["srcs"]) == "" {
				return "capacidade archive_op: create sem fontes"
			}
			if strings.HasPrefix(st.In["srcs"], "$") {
				if !lists[strings.TrimPrefix(st.In["srcs"], "$")] {
					return "capacidade archive_op: srcs não é uma lista produzida por script"
				}
			} else {
				for _, f := range strings.Fields(st.In["srcs"]) {
					if !dsPathLit(f) {
						return "capacidade archive_op: fonte não literal " + f
					}
				}
			}
		case "extract":
			if !dsPathLit(st.In["arc"]) || strings.TrimSpace(st.In["arc"]) == "" {
				return "capacidade archive_op: extract sem arc literal"
			}
			if st.In["dir"] != "" && !dsPathLit(st.In["dir"]) {
				return "capacidade archive_op: dir não literal"
			}
		default:
			return "capacidade archive_op: op tem que ser create ou extract"
		}
	}
	return ""
}

// dsSlotNameRe casa o nome de um slot declarado (mesmo alfabeto do
// placeholder $slot.<nome>).
var dsSlotNameRe = lazyRx(`^[a-z][a-z0-9_]*$`)

// dsKindOK: os tipos de slot que um método pode declarar — os kinds de
// entidade do NLU mais os tipos livres de texto.
var dsKindSet = smSet("file", "dir", "test", "symbol", "service", "container",
	"toolchain", "bin", "ssh_host", "ts_peer", "port", "number",
	"text", "name", "message", "q")

func dsKindOK(k string) bool { return dsKindSet[k] }

// ---------- aplicação do overlay (carga) ----------

// dsApplyNLU funde as intenções e os atalhos aprendidos em m. Intenções
// aprendidas chegam depois das embutidas, com embedN=0: a prior do
// classificador nunca se move por elas (espec 0.14b), então um atalho
// (0.97) é o caminho exato e o classificador só ajuda nas paráfrases.
func dsApplyNLU(m *nuModel, ov *dsOverlay) {
	if m == nil || ov == nil || m.loadErr != nil {
		return
	}
	n := 0
	for _, it := range ov.Intents {
		if n >= dsMaxIntents {
			break
		}
		if dsAddIntent(m, it) {
			n++
		}
	}
	for canon, a := range ov.Aliases {
		if _, ok := m.byName[a.Intent]; !ok || a.Intent == "none" || canon == "" {
			continue
		}
		if fgIsMutating(a.Intent) { // nem à mão: atalho nunca aponta uma mutação
			continue
		}
		if m.aliases == nil {
			m.aliases = map[string]string{}
		}
		m.aliases[canon] = a.Intent
	}
}

// dsAddIntent acrescenta uma intenção aprendida a m (dedup por nome).
func dsAddIntent(m *nuModel, it dsIntent) bool {
	if !dsIntentNameOK(it.Name) || it.Name == "none" || fgIsMutating(it.Name) {
		return false
	}
	if _, dup := m.byName[it.Name]; dup {
		return false
	}
	var exs []string
	for _, e := range it.Examples {
		if t, ok := nuExampleText(e); ok {
			exs = append(exs, t)
		}
	}
	if len(exs) == 0 {
		return false
	}
	m.data.Intents = append(m.data.Intents, nuIntentDef{
		Name: it.Name, Desc: it.Desc, Examples: exs, Slots: it.Slots,
	})
	m.byName[it.Name] = len(m.data.Intents) - 1
	// Prior modesto e tampado: o freeze da spec 0.14b existe pra impedir
	// que exemplos de overlay inflem o prior de uma intenção da gramática
	// — uma intenção APRENDIDA é classe nova, não tem massa embutida pra
	// proteger. Sem prior ela só aparecia como "talvez X, 0.03" e nunca
	// vencia o argmax; com prior ≤3 ela compete quando os tokens casam de
	// verdade e nunca supera uma intenção embutida por prior sozinho.
	pr := len(exs)
	if pr > 3 {
		pr = 3
	}
	m.embedN = append(m.embedN, pr)
	return true
}

// dsLoadNLU lê o overlay de métodos e o aplica a m (chamada de nuGet,
// dentro do mesmo portão nuForgeOn do overlay da gramática).
func dsLoadNLU(m *nuModel) {
	ov, err := dsReadOverlay(dsOverlayPath())
	if err != nil {
		dbg("distill", map[string]any{"overlay": "ignorado", "err": err.Error()})
		return
	}
	dsApplyNLU(m, ov)
}

// dsApplyMethods funde os métodos aprendidos na biblioteca; cada um passa
// por dsValidMethod e por uma intenção que exista (na gramática ou
// aprendida no mesmo overlay). Retorna quantos entraram.
func dsApplyMethods(lib map[string][]mhMethod, ov *dsOverlay) int {
	if ov == nil {
		return 0
	}
	// a intenção do método precisa existir em algum lugar: na biblioteca
	// (tem métodos), entre os aprendidos deste overlay, ou na gramática.
	grammar := map[string]bool{}
	if m := nuGet(); m != nil && m.loadErr == nil {
		for _, it := range m.data.Intents {
			grammar[it.Name] = true
		}
	}
	intentOK := func(name string) bool {
		if grammar[name] {
			return true
		}
		if _, ok := lib[name]; ok {
			return true
		}
		for _, it := range ov.Intents {
			if it.Name == name {
				return true
			}
		}
		return false
	}
	added := 0
	for _, m := range ov.Methods {
		if added >= dsMaxMethods {
			break
		}
		if why := dsValidMethod(m, intentOK); why != "" {
			dbg("distill", map[string]any{"método": m.Name, "ignorado": why})
			continue
		}
		lib[m.Intent] = append(lib[m.Intent], m)
		added++
	}
	if added > 0 {
		for k := range lib {
			l := lib[k]
			sort.SliceStable(l, func(i, j int) bool { return l[i].Priority > l[j].Priority })
			lib[k] = l
		}
	}
	return added
}

// dsLoadMethods lê o overlay e funde os métodos em lib (chamada de
// mhLibrary, dentro do mesmo portão nuForgeOn).
func dsLoadMethods(lib map[string][]mhMethod) {
	ov, err := dsReadOverlay(dsOverlayPath())
	if err != nil {
		dbg("distill", map[string]any{"overlay": "ignorado", "err": err.Error()})
		return
	}
	dsApplyMethods(lib, ov)
}

// ---------- destilação: plano → candidato ----------

// dsCand é uma candidata a método aprendido.
type dsCand struct {
	Plan   pcPlan
	Method mhMethod
	Intent dsIntent          // Name "" quando o método gruda numa intenção existente
	Attach string            // intenção existente que recebe o método ("" = cria Intent)
	Alias  nuAlias           // atalho exato do texto do plano → a intenção
	Bind   map[string]string // valores do plano base p/ cada $slot.x (replay do portão)
}

// dsSkeleton: a mutating plan whose shape is "find produces → per-item
// delete" promotes to a method of verified caps instead of literal bash —
// the trajectory taught the STRUCTURE; the caps re-run confinement,
// permission and per-item verification at execution. nil = no skeleton.
func dsSkeleton(p pcPlan) *mhMethod {
	if p.Wins < dsMinWins {
		return nil
	}
	findCmd, del := "", false
	for _, c := range p.Cmds {
		t := shellFields(c)
		if len(t) == 0 {
			continue
		}
		switch filepath.Base(t[0]) {
		case "find":
			if findCmd == "" && IsReadOnly(c) {
				findCmd = c
			}
		case "rm":
			del = true
		}
		if strings.Contains(c, "-delete") || strings.Contains(c, "xargs rm") {
			del = true
		}
	}
	if findCmd == "" || !del {
		return nil
	}
	// find args → file_find slots: bare path arg → dir, -name glob → name,
	// -type f|d → type, -maxdepth N → max_depth
	in := map[string]string{"limit": "32"}
	t := shellFields(findCmd)
	for i := 1; i < len(t); i++ {
		a := t[i]
		if !strings.HasPrefix(a, "-") && in["path"] == "" && !strings.HasPrefix(t[i-1], "-") {
			in["path"] = strings.TrimPrefix(a, "./")
		}
		if i+1 < len(t) {
			switch a {
			case "-name", "-iname":
				in["name"] = strings.Trim(t[i+1], `'"`)
			case "-type":
				if t[i+1] == "f" || t[i+1] == "d" {
					in["type"] = map[string]string{"f": "file", "d": "dir"}[t[i+1]]
				}
			case "-maxdepth":
				in["max_depth"] = t[i+1]
			}
		}
	}
	return &mhMethod{
		Priority: 7,
		Doc:      "esqueleto aprendido (jazz distill): " + oneLine(p.Task, 80),
		Steps: []mhStep{
			{Cap: "script:file_find", In: in, Out: "cand"},
			{Cap: "for_each", In: map[string]string{"from": "$cand", "do": "file_op", "op": "delete", "max": "32"}},
		},
		Verify: []mhCheck{{Check: "list_gone", Arg: "$cand"}},
		Needs:  []string{"bin.python3"},
		Risk:   "high",
		CostMS: 8000,
	}
}

// dsStripRedirect separa o redirect de stdout do resto do comando:
// "find … > out.txt" → (campos do find, "out.txt", append). Os de erro
// (2>, 2>&1, >&2) são descartados — não são conteúdo de lista. Sem
// redirect de stdout: dest vazio.
func dsStripRedirect(t []string) (inner []string, dest string, app bool) {
	for i := 0; i < len(t); i++ {
		a := t[i]
		if a == ">" || a == ">>" {
			if i+1 >= len(t) {
				return inner, "", false
			}
			return inner, t[i+1], a == ">>"
		}
		if a == "2>" || a == "2>>" {
			i++ // o alvo do erro não é campo do comando
			continue
		}
		if a == "2>&1" || a == ">&2" || a == "1>&2" {
			continue
		}
		inner = append(inner, a)
	}
	return inner, "", false
}

// dsCdTarget reconhece um segmento que é só "cd <dir>" — o prefixo
// mecânico que o modelo põe antes de trabalhar no root.
func dsCdTarget(s string) (string, bool) {
	t := shellFields(s)
	if len(t) == 2 && t[0] == "cd" {
		return strings.Trim(t[1], `'"`), true
	}
	return "", false
}

// dsCapPlan generaliza o esqueleto: percorre os comandos do plano e
// reescreve cada mutação conhecida como passos de capacidade verificada —
// find vira script:file_find (produtor de lista), rm/xargs rm/-delete vira
// for_each+file_op delete sobre a lista produzida, mv/mkdir literais viram
// file_op direto, e sonda só-leitura fica learned_cmd. Só emite método
// quando TODO passo mutante cai numa cap real; um comando sem encaixe
// devolve nil (o plano não vira método de caps). "cd <root>" e "cd ."
// são no-op: os passos de cap já rodam no root — cd pra OUTRO lugar
// muda a semântica e fica sem encaixe (o plano não destila).
func dsCapPlan(p pcPlan, root string) *mhMethod {
	if p.Wins < dsMinWins || len(p.Cmds) == 0 || len(p.Cmds) > dsMaxSteps {
		return nil
	}
	var steps []mhStep
	var verify []mhCheck
	gone := map[string]bool{} // outs de produtores já consumidos por delete
	last, prod, fop, caps := "", 0, 0, 0
	script := false
	emitFind := func(in map[string]string, scr string) {
		prod++
		last = "cand"
		if prod > 1 {
			last = fmt.Sprintf("cand%d", prod)
		}
		steps = append(steps, mhStep{Cap: scr, In: in, Out: last})
		script, caps = true, caps+1
	}
	emitEach := func() {
		steps = append(steps, mhStep{Cap: "for_each",
			In: map[string]string{"from": "$" + last, "do": "file_op", "op": "delete", "max": "32"}})
		gone[last], caps = true, caps+1
	}
	dstdirs := map[string]string{} // produtor → dir do fan-out (list_present)
	emitEachTo := func(op, dir string) {
		steps = append(steps, mhStep{Cap: "for_each",
			In: map[string]string{"from": "$" + last, "do": "file_op", "op": op,
				"to_dir": dir, "max": "32"}})
		if op == "rename" {
			gone[last] = true // move: a origem some (list_gone já cobre)
		} else {
			dstdirs[last] = dir
		}
		caps++
	}
	emitOp := func(in map[string]string, check string) {
		fop++
		out := fmt.Sprintf("f%d", fop)
		steps = append(steps, mhStep{Cap: "file_op", In: in, Out: out})
		verify = append(verify, mhCheck{Check: check, Arg: "$" + out})
		caps++
	}
	top := 0
	// textVerif prova o replace: o texto novo aparece; o velho só sai de
	// cena quando não é substring do novo ("a"→"ab" ainda "contém" a).
	textVerif := func(out, from, to string) {
		if to != "" {
			verify = append(verify, mhCheck{Check: "file_contains", Arg: "$" + out, Arg2: to})
		}
		if !strings.Contains(to, from) {
			verify = append(verify, mhCheck{Check: "file_lacks", Arg: "$" + out, Arg2: from})
		}
	}
	emitText := func(in map[string]string) {
		top++
		out := fmt.Sprintf("t%d", top)
		steps = append(steps, mhStep{Cap: "text_op", In: in, Out: out})
		textVerif(out, in["from_str"], in["to_str"])
		caps++
	}
	lacks := map[string]string{} // produtor → agulha que sumiu (list_lacks)
	emitEachText := func(xin map[string]string) {
		in := map[string]string{"from": "$" + last, "do": "text_op", "max": "32"}
		for k, v := range xin {
			in[k] = v
		}
		steps = append(steps, mhStep{Cap: "for_each", In: in, Out: ""})
		if f, t := xin["from_str"], xin["to_str"]; f != "" && !strings.Contains(t, f) {
			lacks[last] = f
		}
		caps++
	}
	aop := 0
	emitArchive := func(in map[string]string, check string) {
		aop++
		out := fmt.Sprintf("a%d", aop)
		steps = append(steps, mhStep{Cap: "archive_op", In: in, Out: out})
		verify = append(verify, mhCheck{Check: check, Arg: "$" + out})
		caps++
	}
	pop := 0
	emitPerm := func(in map[string]string, check, arg2 string) {
		pop++
		out := fmt.Sprintf("p%d", pop)
		steps = append(steps, mhStep{Cap: "perm_op", In: in, Out: out})
		c := mhCheck{Check: check, Arg: "$" + out}
		switch check {
		case "file_mode", "list_mode", "file_owner", "file_group",
			"list_owner", "list_group", "file_perm", "file_noperm", "list_perm":
			c.Arg2 = arg2
		}
		verify = append(verify, c)
		caps++
	}
	sav := 0
	emitSave := func(dest string, sorted bool) {
		sav++
		out := fmt.Sprintf("s%d", sav)
		in := map[string]string{"from": "$" + last, "path": dest}
		if sorted {
			in["sort"] = "true"
		}
		steps = append(steps, mhStep{Cap: "save_list", In: in, Out: out})
		verify = append(verify, mhCheck{Check: "file_exists", Arg: "$" + out})
		caps++
	}
	literal := func(c string) bool { // sonda: fica learned_cmd quando reproduzível
		if dsCmdOK(c) != "" || !dsReplaySafe(c) {
			return false
		}
		steps = append(steps, mhStep{Cap: "learned_cmd", In: map[string]string{"cmd": c}})
		return true
	}
	// mapSeg mapeia UM comando (já separado) para os passos de cap; false =
	// sem encaixe e o plano inteiro não destila.
	mapSeg := func(s string) bool {
		t := shellFields(s)
		inner, dest, _ := dsStripRedirect(t)
		if dest != "" {
			// "find … > arquivo" salva a lista produzida — só esse
			// produtor tem cap; o resto não finge encaixe.
			if len(inner) > 0 && filepath.Base(inner[0]) == "find" && dsPathLit(dest) {
				in, _, scr, del, _, _, ok := dsFindIn(inner)
				if !ok || del {
					return false
				}
				emitFind(in, scr)
				emitSave(dest, false)
				return true
			}
			return false
		}
		t = inner
		if len(t) == 0 {
			return false
		}
		switch filepath.Base(t[0]) {
		case "find":
			in, xin, scr, del, xop, xdir, ok := dsFindIn(t)
			if !ok { // find com flag sem encaixe: read-only fica literal
				return literal(s)
			}
			emitFind(in, scr)
			switch {
			case del:
				emitEach()
			case xin != nil:
				emitEachText(xin)
			case xop != "":
				emitEachTo(xop, xdir)
			}
		case "rm":
			args, ok := dsRmArgs(t)
			if !ok {
				return false
			}
			if last != "" && !gone[last] {
				emitEach() // alvos do rm eram a lista produzida
				return true
			}
			if len(args) == 0 {
				return last != "" // rm sem alvo: vale só se já consumiu a lista
			}
			for _, a := range args {
				if !dsPathLit(a) {
					return false
				}
				emitOp(map[string]string{"op": "delete", "path": a}, "file_gone")
			}
		case "mv":
			args, ok := dsFlagArgs(t, "fv")
			if !ok || len(args) < 2 {
				return false
			}
			if len(args) == 2 {
				if !dsPathLit(args[0]) || !dsPathLit(args[1]) {
					return false
				}
				// o val do file_op rename é a origem — sumir dela prova o rename
				emitOp(map[string]string{"op": "rename", "path": args[0], "to": args[1]}, "file_gone")
				return true
			}
			// mv multi-fonte: último arg é o dir — fan-out literal de renames
			dir := args[len(args)-1]
			if !dsPathLit(dir) {
				return false
			}
			for _, a := range args[:len(args)-1] {
				if !dsPathLit(a) {
					return false
				}
				emitOp(map[string]string{"op": "rename", "path": a, "to_dir": dir}, "file_gone")
			}
		case "cp":
			// -r/-R ok: brCopyTree copia árvore confinada dos dois lados
			args, ok := dsFlagArgs(t, "rvfRn")
			if !ok || len(args) < 2 {
				return false
			}
			if len(args) == 2 {
				if !dsPathLit(args[0]) || !dsPathLit(args[1]) {
					return false
				}
				emitOp(map[string]string{"op": "copy", "path": args[0], "to": args[1]}, "file_exists")
				return true
			}
			dir := args[len(args)-1]
			if !dsPathLit(dir) {
				return false
			}
			for _, a := range args[:len(args)-1] {
				if !dsPathLit(a) {
					return false
				}
				emitOp(map[string]string{"op": "copy", "path": a, "to_dir": dir}, "file_exists")
			}
		case "ln":
			op, args, ok := dsLnArgs(t)
			if !ok {
				return false
			}
			// ln <alvo> <link>: path é o link criado (confinado), to é o alvo
			emitOp(map[string]string{"op": op, "path": args[1], "to": args[0]}, "file_exists")
		case "sed":
			// sed -i[.bak] 's/a/b/g' arq [arq…]: cada arquivo literal é um
			// text_op — sem -i (stdout) o mapSeg não encaixa e cai literal.
			if len(t) < 3 {
				return false
			}
			var flags, files []string
			var expr string
			for _, a := range t[1:] {
				switch {
				case a == "-i" || strings.HasPrefix(a, "-i"):
					flags = append(flags, a)
				case a == "-e":
				case strings.HasPrefix(a, "-"):
					return false
				case expr == "" && (len(a) > 2 && a[0] == 's'):
					expr = a
				default:
					files = append(files, a)
				}
			}
			if len(flags) == 0 || expr == "" || len(files) == 0 {
				return false // sem -i o sed só emite no stdout — não edita
			}
			// flags+expr validam juntos pelo dsSedArgs (mesmo shape)
			sargs := append(append([]string{"sed"}, flags...), expr)
			sin, sok := dsSedArgs(sargs)
			if !sok {
				return false
			}
			for _, f := range files {
				if !dsPathLit(f) {
					return false
				}
				fin := map[string]string{"path": f}
				for k, v := range sin {
					fin[k] = v
				}
				emitText(fin)
			}
		case "mkdir":
			args, ok := dsMkdirArgs(t)
			if !ok {
				return false
			}
			for _, a := range args {
				if !dsPathLit(a) {
					return false
				}
				emitOp(map[string]string{"op": "mkdir", "path": a}, "file_exists")
			}
		case "chmod":
			rec, mode, paths, ok := dsChmodArgs(t)
			if !ok {
				return false
			}
			for _, p := range paths {
				in := map[string]string{"op": "chmod", "path": p, "mode": mode}
				if rec {
					in["recursive"] = "true"
				}
				check := "file_exists" // simbólico prova-se pela execução + evidência
				if _, isOct := brParseOctal(mode); isOct {
					check = "file_mode"
				}
				emitPerm(in, check, mode)
			}
		case "chown", "chgrp":
			rec, owner, group, paths, ok := dsChownArgs(t)
			if !ok {
				return false
			}
			if t[0] == "chgrp" {
				owner, group = "", owner // chgrp: o spec é o grupo
			}
			for _, p := range paths {
				in := map[string]string{"op": t[0], "path": p, "owner": owner, "group": group}
				if rec {
					in["recursive"] = "true"
				}
				if owner != "" {
					emitPerm(in, "file_owner", owner)
				} else {
					emitPerm(in, "file_group", group)
				}
			}
		case "tar":
			in, check, ok := dsTarArgs(t)
			if !ok {
				return false
			}
			emitArchive(in, check)
		case "zip":
			in, ok := dsZipArgs(t)
			if !ok {
				return false
			}
			emitArchive(in, "file_exists")
		case "unzip":
			in, ok := dsUnzipArgs(t)
			if !ok {
				return false
			}
			emitArchive(in, "list_exists")
		default:
			return literal(s)
		}
		return true
	}
	for _, c := range p.Cmds {
		var parts []string
		// \; (o terminador do find -exec) não é separador de sequência —
		// protege antes do split e devolve como token ";" pro mapSeg.
		protected := strings.ReplaceAll(c, "\\;", "\x00")
		for _, s := range reSplit.Split(protected, -1) {
			if s = strings.TrimSpace(strings.ReplaceAll(s, "\x00", ";")); s != "" {
				parts = append(parts, s)
			}
		}
		if len(parts) == 0 {
			return nil
		}
		if strings.Contains(c, "|") {
			// pipelines com encaixe: find … | xargs rm (produtor+deletor),
			// find … | tee arq e find … | sort > arq (produtor+save_list).
			// Todo o resto fica literal ou nada.
			if len(parts) == 2 {
				l, r := shellFields(parts[0]), shellFields(parts[1])
				rIn, rDest, _ := dsStripRedirect(r)
				if len(l) > 0 && len(rIn) > 0 && filepath.Base(l[0]) == "find" {
					switch filepath.Base(rIn[0]) {
					case "xargs":
						if dsXargsRm(rIn) {
							in, _, scr, del, _, _, ok := dsFindIn(l)
							if !ok || del {
								return nil
							}
							emitFind(in, scr)
							emitEach()
							continue
						}
						if xop, dir, ok := dsXargsCpMv(rIn); ok {
							in, _, scr, del, _, _, ok2 := dsFindIn(l)
							if !ok2 || del {
								return nil
							}
							emitFind(in, scr)
							emitEachTo(xop, dir)
							continue
						}
						if xin, ok := dsXargsSed(rIn); ok {
							in, _, scr, del, _, _, ok2 := dsFindIn(l)
							if !ok2 || del {
								return nil
							}
							emitFind(in, scr)
							emitEachText(xin)
							continue
						}
						break
					case "tee":
						if len(rIn) == 2 && rDest == "" && dsPathLit(rIn[1]) {
							in, _, scr, del, _, _, ok := dsFindIn(l)
							if !ok || del {
								return nil
							}
							emitFind(in, scr)
							emitSave(rIn[1], false)
							continue
						}
					case "sort":
						if len(rIn) == 1 && rDest != "" && dsPathLit(rDest) {
							in, _, scr, del, _, _, ok := dsFindIn(l)
							if !ok || del {
								return nil
							}
							emitFind(in, scr)
							emitSave(rDest, true)
							continue
						}
					}
				}
			}
			if !literal(c) {
				return nil
			}
			continue
		}
		// sequência (; &&): cada comando vale um passo — os passos já rodam
		// em ordem e abortam na falha, mesma semântica do &&.
		for _, s := range parts {
			if cd, ok := dsCdTarget(s); ok && (cd == "." || cd == "" ||
				filepath.Clean(cd) == filepath.Clean(root)) {
				continue // "cd <root>;"/"cd ." é no-op: cap roda no root
			}
			if !mapSeg(s) {
				return nil
			}
		}
	}
	if caps == 0 {
		return nil // sem passo de cap não é cap-plan — o caminho literal cobre
	}
	for l := range gone {
		verify = append(verify, mhCheck{Check: "list_gone", Arg: "$" + l})
	}
	for l, dir := range dstdirs {
		verify = append(verify, mhCheck{Check: "list_present", Arg: "$" + l, Arg2: dir})
	}
	for l, needle := range lacks {
		verify = append(verify, mhCheck{Check: "list_lacks", Arg: "$" + l, Arg2: needle})
	}
	if v := dsVerifyCmd(p); v != "" && p.Cmds[len(p.Cmds)-1] != v &&
		dsCmdOK(v) == "" && dsReplaySafe(v) {
		steps = append(steps, mhStep{Cap: "learned_cmd", In: map[string]string{"cmd": v}, Out: "vrf"})
		verify = append(verify, mhCheck{Check: "exit_zero", Arg: "$vrf"})
	}
	if len(verify) == 0 || len(steps) > dsMaxSteps {
		return nil
	}
	risk := "med"
	for _, st := range steps {
		if st.Cap == "for_each" || (st.Cap == "file_op" && st.In["op"] == "delete") {
			risk = "high"
		}
	}
	m := &mhMethod{
		Priority: 7,
		Doc:      "caps aprendidas (jazz distill): " + oneLine(p.Task, 80),
		Steps:    steps,
		Verify:   verify,
		Risk:     risk,
		CostMS:   len(steps) * 2000,
	}
	if script {
		m.Needs = []string{"bin.python3"}
	}
	return m
}

// dsPathLit: alvo literal de file_op — caminho relativo simples, sem os
// metacaracteres que indicariam expansão de shell ou fuga do projeto
// (brSafePath confina de novo na execução; aqui é só higiene do aprendido).
func dsPathLit(s string) bool {
	if s == "" || strings.HasPrefix(s, "-") || strings.HasPrefix(s, "~") || filepath.IsAbs(s) {
		return false
	}
	if strings.Contains(s, "..") || strings.ContainsAny(s, "$`*?(){}[]'\"\\") {
		return false
	}
	return true
}

// dsRmArgs: rm só vira cap com flags benignos (-f/-v/-d); -r/-R/-i não têm
// encaixe (file_op delete não é recursivo, -i é interativo). Retorna os
// alvos posicionais.
func dsRmArgs(t []string) ([]string, bool) {
	return dsFlagArgs(t, "fvd")
}

// dsMvArgs: "mv [-f|-v] origem destino" — no-clobber/update/interativo não
// têm encaixe no file_op rename, nem fan-out pra diretório (>2 alvos).
func dsMvArgs(t []string) ([]string, bool) {
	args, ok := dsFlagArgs(t, "fv")
	if !ok || len(args) != 2 || !dsPathLit(args[0]) || !dsPathLit(args[1]) {
		return nil, false
	}
	return args, true
}

// dsMkdirArgs: "mkdir [-p|-v] dir…" — modo/dono não têm encaixe.
func dsMkdirArgs(t []string) ([]string, bool) {
	args, ok := dsFlagArgs(t, "pv")
	if !ok || len(args) == 0 {
		return nil, false
	}
	return args, true
}

// dsLnArgs: "ln [-s|-f|-v] alvo link" — só link simbólico ou hard de um
// alvo pra um nome; -P/-L/-b/-t/-i não têm encaixe. O link (criado no
// projeto) é literal; o alvo pode ser literal OU absoluto simples (o
// texto do link aponta pra fora sem escrever fora).
func dsLnArgs(t []string) (string, []string, bool) {
	op := "link"
	args := []string{}
	for i := 1; i < len(t); i++ {
		a := t[i]
		if a == "--" {
			args = append(args, t[i+1:]...)
			break
		}
		if strings.HasPrefix(a, "-") {
			for _, f := range a[1:] {
				switch f {
				case 's':
					op = "symlink"
				case 'f', 'v', 'n':
				default:
					return "", nil, false
				}
			}
			continue
		}
		args = append(args, a)
	}
	if len(args) != 2 || !dsPathLit(args[1]) {
		return "", nil, false
	}
	// alvo do link: literal relativo OU absoluto sem metacaracteres —
	// confinado é o link, não o texto que ele carrega.
	if !dsPathLit(args[0]) {
		if !filepath.IsAbs(args[0]) || strings.ContainsAny(args[0], "$`*?(){}[]'\"\\") || strings.Contains(args[0], "..") {
			return "", nil, false
		}
	}
	return op, args, true
}

// dsFlagArgs separa flags de uma letra (todos precisam estar em allow) dos
// argumentos posicionais; "--" encerra os flags.
func dsFlagArgs(t []string, allow string) ([]string, bool) {
	var args []string
	for i := 1; i < len(t); i++ {
		a := t[i]
		if a == "--" {
			args = append(args, t[i+1:]...)
			break
		}
		if strings.HasPrefix(a, "-") {
			for _, f := range a[1:] {
				if !strings.ContainsRune(allow, f) {
					return nil, false
				}
			}
			continue
		}
		args = append(args, a)
	}
	return args, true
}

// dsXargsRm: "xargs [flags] rm [flags benignos]" — os alvos vêm da entrada,
// então depois do rm só flags são aceitos.
func dsXargsRm(t []string) bool {
	i := 1
	for ; i < len(t); i++ {
		a := t[i]
		if !strings.HasPrefix(a, "-") {
			break
		}
		switch a {
		case "-n", "-I", "-P", "-d", "-s", "-L", "--max-args", "--max-procs", "--delimiter", "--max-lines":
			i++ // flag com argumento próprio
		}
	}
	if i >= len(t) || filepath.Base(t[i]) != "rm" {
		return false
	}
	for i++; i < len(t); i++ {
		a := t[i]
		if !strings.HasPrefix(a, "-") {
			return false // alvo literal não vem da lista produzida
		}
		for _, f := range a[1:] {
			if f != 'f' && f != 'v' && f != 'd' {
				return false
			}
		}
	}
	return true
}

// dsXargsCpMv: "xargs [flags] (cp|mv) [-t dir | -I{} … {} dir]" — o
// fan-out de move/copy pra um dir alimentado pela lista produzida.
// devolve o op do file_op e o dir destino; ok=false = sem encaixe.
func dsXargsCpMv(t []string) (op, dir string, ok bool) {
	i := 1
	repl := ""
	for ; i < len(t); i++ {
		a := t[i]
		if !strings.HasPrefix(a, "-") {
			break
		}
		// "-I{}" grudado: o valor vem no mesmo token
		if strings.HasPrefix(a, "-I") && len(a) > 2 {
			repl = a[2:]
			continue
		}
		switch a {
		case "-I", "-n", "-P", "-d", "-s", "-L", "--max-args", "--max-procs", "--delimiter", "--max-lines", "-i":
			if i+1 < len(t) {
				if a == "-I" || a == "-i" {
					repl = t[i+1]
				}
				i++
			}
		}
	}
	if i >= len(t) {
		return "", "", false
	}
	cmd := filepath.Base(t[i])
	if cmd != "cp" && cmd != "mv" {
		return "", "", false
	}
	op = map[string]string{"cp": "copy", "mv": "rename"}[cmd]
	rest := t[i+1:]
	if repl != "" {
		// "xargs -I{} cp {} dir": o {} é o item, o outro arg literal é o dir
		var lit []string
		for _, a := range rest {
			if strings.HasPrefix(a, "-") {
				for _, f := range a[1:] {
					if f != 'f' && f != 'v' && f != 'n' {
						return "", "", false
					}
				}
				continue
			}
			if a == repl {
				continue
			}
			lit = append(lit, a)
		}
		if len(lit) != 1 || !dsPathLit(lit[0]) {
			return "", "", false
		}
		return op, lit[0], true
	}
	// "xargs cp -t dir": o -t leva o dir; sem -t não há encaixe
	for j, a := range rest {
		if (a == "-t" || a == "--target-directory") && j+1 < len(rest) {
			if !dsPathLit(rest[j+1]) {
				return "", "", false
			}
			return op, rest[j+1], true
		}
	}
	return "", "", false
}

// dsFindIn: os argumentos de um find (tokens depois do "find") viram as
// entradas do script:file_find — espelha os slots do delete_file.each.
// del = o próprio find já deletava (-delete, -exec rm). ok=false = sobrou
// flag sem encaixe (o comando fica literal quando dá, senão não destila).
// dsPermKeys: predicados que só o script:perm_find entende — se um find
// mistura esses com filtros de nome/tamanho/tempo, não cabe em nenhum
// script sozinho e falha honesto.
var dsPermKeys = map[string]bool{
	"mode": true, "not_mode": true, "world_writable": true,
	"not_world_writable": true, "suid": true, "sgid": true,
	"executable": true, "not_executable": true, "owner": true,
	"not_owner": true, "group": true, "not_group": true,
	"no_owner": true, "no_group": true, "orphan": true,
}

func dsFindIn(t []string) (in, xin map[string]string, scr string, del bool, xop, xdir string, ok bool) {
	in = map[string]string{"limit": "32"}
	paths := 0
	sawPrune := false
	num := func(s string) bool {
		n, err := strconv.Atoi(s)
		return err == nil && n >= 0
	}
	arg := func(i int) string {
		if i+1 < len(t) {
			return t[i+1]
		}
		return ""
	}
	for i := 1; i < len(t); i++ {
		a := t[i]
		switch {
		case a == "-delete":
			del = true
		case a == "-exec" || a == "-execdir":
			j := i + 1
			if j >= len(t) {
				return nil, nil, "", false, "", "", false
			}
			ecmd := filepath.Base(t[j])
			if ecmd == "cp" || ecmd == "mv" {
				// "-exec (cp|mv) [-f|-v|-n] {} dir ;|+" — fan-out pra dir
				var lit []string
				for j++; j < len(t) && t[j] != ";" && t[j] != "+"; j++ {
					a2 := t[j]
					if strings.HasPrefix(a2, "-") {
						for _, f := range a2[1:] {
							if f != 'f' && f != 'v' && f != 'n' {
								return nil, nil, "", false, "", "", false
							}
						}
						continue
					}
					if strings.Contains(a2, "{}") {
						continue
					}
					lit = append(lit, a2)
				}
				if j >= len(t) || len(lit) != 1 || !dsPathLit(lit[0]) {
					return nil, nil, "", false, "", "", false
				}
				xop = map[string]string{"cp": "copy", "mv": "rename"}[ecmd]
				xdir, i = lit[0], j
				continue
			}
			if ecmd == "sed" {
				// "-exec sed -i 's/a/b/g' {} ;|+": cada item recebe o
				// text_op replace — {} é o único arg-arquivo permitido.
				var sargs []string
				seenPh := false
				for j++; j < len(t) && t[j] != ";" && t[j] != "+"; j++ {
					if strings.Contains(t[j], "{}") {
						if t[j] != "{}" || seenPh {
							return nil, nil, "", false, "", "", false
						}
						seenPh = true
						continue
					}
					sargs = append(sargs, t[j])
				}
				if j >= len(t) || !seenPh {
					return nil, nil, "", false, "", "", false
				}
				sin, sok := dsSedArgs(append([]string{"sed"}, sargs...))
				if !sok {
					return nil, nil, "", false, "", "", false
				}
				xop, xin, i = "replace", sin, j
				continue
			}
			if ecmd != "rm" {
				return nil, nil, "", false, "", "", false
			}
			for j++; j < len(t) && t[j] != ";" && t[j] != "+"; j++ {
				if t[j] != "-f" && t[j] != "-v" && t[j] != "--" && !strings.Contains(t[j], "{}") {
					return nil, nil, "", false, "", "", false
				}
			}
			if j >= len(t) {
				return nil, nil, "", false, "", "", false
			}
			del, i = true, j
		case a == "-name" || a == "-iname" || a == "-path":
			if arg(i) == "" {
				return nil, nil, "", false, "", "", false
			}
			// "-name|-path X -prune": podar subárvore. file_find já pula
			// .git/node_modules/vendor — podar o pulado é neutro; podar
			// outro nome vira o exclude de basename/relpath.
			if arg(i+1) == "-prune" {
				tgt := strings.TrimPrefix(arg(i), "./")
				if !dsFindSkips(tgt) {
					if in["exclude"] != "" {
						return nil, nil, "", false, "", "", false
					}
					in["exclude"] = tgt
				}
				sawPrune, i = true, i+2
				continue
			}
			// "-path" sozinho é glob em relpath — sem input no file_find
			if a == "-path" || in["name"] != "" {
				return nil, nil, "", false, "", "", false
			}
			in["name"], i = arg(i), i+1
		case a == "-prune":
			// "-prune" sem -name/-path antes poda tudo — sem sentido aqui
			return nil, nil, "", false, "", "", false
		case a == "-o":
			// "-name X -prune -o <resto>": o OR de top-level depois do
			// grupo de poda é neutro — o resto é o predicado real
			if !sawPrune {
				return nil, nil, "", false, "", "", false
			}
			sawPrune = false
		case a == "(" || a == "\\(":
			// \( -name g1 -o -name g2 \) → name_re com a união dos globs
			var globs []string
			j := i + 1
			for j+1 < len(t) && (t[j] == "-name" || t[j] == "-iname") {
				globs = append(globs, t[j+1])
				j += 2
				if j < len(t) && t[j] == "-o" {
					j++
					continue
				}
				break
			}
			if len(globs) < 1 || j >= len(t) || (t[j] != ")" && t[j] != "\\)") {
				return nil, nil, "", false, "", "", false
			}
			if len(globs) == 1 {
				if in["name"] != "" {
					return nil, nil, "", false, "", "", false
				}
				in["name"] = globs[0]
			} else {
				if in["name"] != "" || in["name_re"] != "" {
					return nil, nil, "", false, "", "", false
				}
				re, okRe := dsGlobAlt(globs)
				if !okRe {
					return nil, nil, "", false, "", "", false
				}
				in["name_re"] = re
			}
			i = j
		case a == "-type":
			switch arg(i) {
			case "f":
				in["type"] = "file"
			case "d":
				in["type"] = "dir"
			default:
				return nil, nil, "", false, "", "", false
			}
			i++
		case a == "-maxdepth" || a == "-mindepth":
			if !num(arg(i)) {
				return nil, nil, "", false, "", "", false
			}
			in[map[string]string{"-maxdepth": "max_depth", "-mindepth": "min_depth"}[a]], i = arg(i), i+1
		case a == "-size":
			n, sign, okSz := dsFindSize(arg(i))
			if !okSz {
				return nil, nil, "", false, "", "", false
			}
			if sign == '+' {
				in["min_size"] = strconv.FormatInt(n, 10)
			} else {
				in["max_size"] = strconv.FormatInt(n, 10)
			}
			i++
		case a == "-mtime" || a == "-mmin":
			f := int64(1)
			if a == "-mtime" {
				f = 1440 // dias → minutos do newer_min/older_min
			}
			v, key := arg(i), "older_min"
			var n int64
			var err error
			switch {
			case strings.HasPrefix(v, "+"):
				n, err = strconv.ParseInt(v[1:], 10, 64)
				n++
			case strings.HasPrefix(v, "-"):
				n, err = strconv.ParseInt(v[1:], 10, 64)
				key = "newer_min"
			default:
				n, err = strconv.ParseInt(v, 10, 64)
			}
			if err != nil || n < 0 {
				return nil, nil, "", false, "", "", false
			}
			in[key], i = strconv.FormatInt(n*f, 10), i+1
		case a == "-empty":
			in["empty"] = "true"
		case a == "-perm" || a == "-user" || a == "-group" || a == "-nouser" || a == "-nogroup":
			key, adv := dsPermFlag(t, i)
			if key == "" {
				return nil, nil, "", false, "", "", false
			}
			if in[key] != "" {
				return nil, nil, "", false, "", "", false
			}
			v := "true"
			if key == "mode" || key == "owner" || key == "group" {
				v = arg(i)
			}
			in[key], i = v, i+adv
		case a == "!" || a == "-not":
			// negação vale pra exclusão de nome/caminho e pra predicados perm
			if i+2 >= len(t) {
				return nil, nil, "", false, "", "", false
			}
			switch t[i+1] {
			case "-name", "-iname", "-path":
				if in["exclude"] != "" {
					return nil, nil, "", false, "", "", false
				}
				in["exclude"], i = t[i+2], i+2
			case "-perm", "-user", "-group":
				key, _ := dsPermFlag(t, i+1)
				nkey := map[string]string{
					"mode": "not_mode", "owner": "not_owner", "group": "not_group",
					"world_writable": "not_world_writable", "executable": "not_executable",
				}[key]
				if nkey == "" || in[nkey] != "" {
					return nil, nil, "", false, "", "", false
				}
				v := "true"
				if nkey == "not_mode" || nkey == "not_owner" || nkey == "not_group" {
					v = t[i+2]
				}
				in[nkey], i = v, i+2
			default:
				return nil, nil, "", false, "", "", false
			}
		case a == "-print" || a == "-print0" || a == "-xdev" || a == "-mount" ||
			a == "-quit" || a == "-true" || a == "-depth":
			// neutros: saída e travessia não mudam o conjunto produzido
		case strings.HasPrefix(a, "-"):
			return nil, nil, "", false, "", "", false // flag sem encaixe
		default:
			// raiz da busca: um único caminho posicional
			if paths > 0 {
				return nil, nil, "", false, "", "", false
			}
			paths++
			if p := strings.TrimPrefix(a, "./"); p != "" && p != "." {
				in["path"] = p
			}
		}
	}
	// predicados perm roteiam pro perm_find; misturados com filtros de
	// nome/tamanho/tempo não cabem em nenhum script sozinho
	ff, pf := false, false
	for k := range in {
		if k == "limit" || k == "path" || k == "type" {
			continue // chaves que os dois scripts compartilham
		}
		if dsPermKeys[k] {
			pf = true
		} else {
			ff = true
		}
	}
	if pf && ff {
		return nil, nil, "", false, "", "", false
	}
	scr = "script:file_find"
	if pf {
		scr = "script:perm_find"
	}
	return in, xin, scr, del, xop, xdir, true
}

// dsSedExpr: uma expressão "s/a/b/flags" literal do sed vira os inputs do
// text_op replace — só formas puramente literais (nada de &, \1, classes
// [ ], âncoras ^$ ou .* do lado do padrão, que mudariam a leitura).
// flags: g (todas), i (ignore_case), vazio (primeira por linha); outras
// formas (Nd, /re/d, y//) não são replace — não encaixam.
func dsSedExpr(e string) (in map[string]string, ok bool) {
	if len(e) < 5 || e[0] != 's' {
		return nil, false
	}
	d := e[1]
	if d == '\\' || d == '/' && false {
		return nil, false
	}
	parts := strings.Split(e[2:], string(d))
	if len(parts) < 3 {
		return nil, false
	}
	from, to, flags := parts[0], parts[1], parts[2]
	if from == "" || strings.ContainsAny(from, "\\&^$*+?[](){}|") {
		return nil, false // padrão não-literal — leitura diferente
	}
	if strings.ContainsAny(to, "\\&") {
		return nil, false // & expande o match, \1 backref — não literal
	}
	in = map[string]string{"op": "replace", "from_str": from, "to_str": to}
	switch flags {
	case "g":
	case "gi", "ig":
		in["ignore_case"] = "true"
	case "i":
		in["ignore_case"], in["first_per_line"] = "true", "true"
	case "":
		in["first_per_line"] = "true"
	default:
		return nil, false
	}
	return in, true
}

// dsXargsSed: "xargs [-I{}] sed -i 's/a/b/g'" → os inputs do text_op que
// cada item produzido alimenta (path vem do item, não do comando).
func dsXargsSed(t []string) (in map[string]string, ok bool) {
	i := 1
	for ; i < len(t); i++ {
		a := t[i]
		if !strings.HasPrefix(a, "-") {
			break
		}
		switch a {
		case "-I", "-n", "-P", "-d", "-s", "-L", "-i":
			if i+1 < len(t) {
				i++
			}
		case "-0", "--null", "-r", "--no-run-if-empty", "-t":
		default:
			return nil, false
		}
	}
	if i >= len(t) || filepath.Base(t[i]) != "sed" {
		return nil, false
	}
	return dsSedArgs(t[i:])
}

// dsSedArgs: "sed [-i[.bak]] [-e] 's/a/b/g'" → inputs do text_op.
// Só -i (edição in-place, sufixo opcional de backup) e -e valem;
// -n/-r/-E/scripts múltiplos mudam a leitura — não encaixam.
func dsSedArgs(t []string) (in map[string]string, ok bool) {
	var expr, bak string
	for i := 1; i < len(t); i++ {
		a := t[i]
		switch {
		case a == "-e" && i+1 < len(t):
			i++
			continue // a expressão vem no arg seguinte — cai no default
		case a == "-i" || strings.HasPrefix(a, "-i"):
			if len(a) > 2 {
				bak = a[2:]
				if !dsPathLit("x"+bak) || strings.ContainsAny(bak, "/\\") {
					return nil, false
				}
			}
		case strings.HasPrefix(a, "-"):
			return nil, false
		default:
			if expr != "" {
				return nil, false // mais de uma expressão/arquivo sobrando
			}
			expr = a
		}
	}
	if expr == "" {
		return nil, false
	}
	in, ok = dsSedExpr(expr)
	if !ok {
		return nil, false
	}
	if bak != "" {
		in["backup_ext"] = bak
	}
	return in, true
}

// dsTarArgs: "tar -c[zgjv]f out src…" → create; "tar -x[zgjv]f arc [-C dir]"
// → extract. Flags proibidas: -O/--to-stdout (transform), --to-command,
// -P/--absolute-names (escapa do root), -T/--files-from, --exclude*
// (seleção que o cap não expressa). Paths têm que ser literais.
func dsTarArgs(t []string) (in map[string]string, check string, ok bool) {
	if len(t) < 3 {
		return nil, "", false
	}
	mode, out, dir := byte(0), "", ""
	var files []string
	i := 1
	for ; i < len(t); i++ {
		a := t[i]
		if a == "-C" {
			if i+1 >= len(t) {
				return nil, "", false
			}
			dir = t[i+1]
			i++
			continue
		}
		if !strings.HasPrefix(a, "-") {
			break
		}
		fl := strings.TrimPrefix(a, "-")
		for _, f := range fl {
			switch f {
			case 'c', 'x', 't':
				if mode != 0 {
					return nil, "", false
				}
				mode = byte(f)
			case 'f', 'z', 'g', 'j', 'J', 'v', 'k', 'p':
			default:
				return nil, "", false // -O/-P/-T/--to-command etc
			}
		}
		if strings.HasPrefix(a, "--") {
			return nil, "", false
		}
	}
	if i >= len(t) {
		return nil, "", false
	}
	out = t[i]
	i++
	// o -C pode vir depois do arquivo ("tar -xzf arc -C dir")
	for ; i < len(t); i++ {
		if t[i] == "-C" {
			if i+1 >= len(t) {
				return nil, "", false
			}
			dir = t[i+1]
			i++
			continue
		}
		files = append(files, t[i])
	}
	switch mode {
	case 'c':
		if dir != "" {
			return nil, "", false // -C no create muda o nome dos membros — não mapeia
		}
		if len(files) == 0 || !dsPathLit(out) {
			return nil, "", false
		}
		for _, f := range files {
			if !dsPathLit(f) {
				return nil, "", false
			}
		}
		return map[string]string{
			"op": "create", "out": out, "srcs": strings.Join(files, " "),
		}, "file_exists", true
	case 'x':
		if !dsPathLit(out) {
			return nil, "", false
		}
		if dir != "" && !dsPathLit(dir) {
			return nil, "", false
		}
		if len(files) != 0 {
			return nil, "", false // extração de membros específicos não mapeia
		}
		in = map[string]string{"op": "extract", "arc": out}
		if dir != "" {
			in["dir"] = dir
		}
		return in, "list_exists", true
	}
	return nil, "", false // 't' (list) e modo ausente não encaixam
}

// dsZipArgs: "zip [-r] out.zip src…" → create com saída .zip.
func dsZipArgs(t []string) (in map[string]string, ok bool) {
	if len(t) < 3 {
		return nil, false
	}
	i := 1
	for ; i < len(t); i++ {
		a := t[i]
		if !strings.HasPrefix(a, "-") {
			break
		}
		switch a {
		case "-r", "-q", "-9", "-6", "-1":
		default:
			return nil, false // -e (senha), -x (exclude), -d (delete) não mapeiam
		}
	}
	if i >= len(t) {
		return nil, false
	}
	out := t[i]
	if !strings.HasSuffix(out, ".zip") || !dsPathLit(out) {
		return nil, false
	}
	var files []string
	for _, f := range t[i+1:] {
		if !dsPathLit(f) {
			return nil, false
		}
		files = append(files, f)
	}
	if len(files) == 0 {
		return nil, false
	}
	return map[string]string{"op": "create", "out": out, "srcs": strings.Join(files, " ")}, true
}

// dsUnzipArgs: "unzip arc.zip [-d dir]" → extract.
func dsUnzipArgs(t []string) (in map[string]string, ok bool) {
	if len(t) < 2 {
		return nil, false
	}
	in = map[string]string{"op": "extract"}
	i := 1
	for ; i < len(t); i++ {
		a := t[i]
		switch {
		case a == "-d":
			if i+1 >= len(t) || !dsPathLit(t[i+1]) {
				return nil, false
			}
			in["dir"] = t[i+1]
			i++
		case a == "-q" || a == "-o" || a == "-qq":
		case strings.HasPrefix(a, "-"):
			return nil, false
		default:
			if in["arc"] != "" {
				return nil, false // membro específico não mapeia
			}
			if !strings.HasSuffix(a, ".zip") || !dsPathLit(a) {
				return nil, false
			}
			in["arc"] = a
		}
	}
	if in["arc"] == "" {
		return nil, false
	}
	return in, true
}

// dsPermShape: forma comum de um passo perm_op — direto (wantPath=true,
// o path vem literal) ou via for_each (o path é injetado por item).
func dsPermShape(in map[string]string, wantPath bool) string {
	op := in["op"]
	if !dsPermOps[op] {
		return "op desconhecido " + op
	}
	if wantPath && strings.TrimSpace(in["path"]) == "" {
		return "falta path"
	}
	if wantPath && !dsPathLit(in["path"]) {
		return "path não literal"
	}
	switch op {
	case "chmod":
		if strings.TrimSpace(in["mode"]) == "" {
			return "chmod sem mode"
		}
	case "chown":
		if strings.TrimSpace(in["owner"]) == "" && strings.TrimSpace(in["group"]) == "" {
			return "chown sem owner/group"
		}
	case "chgrp":
		if strings.TrimSpace(in["group"]) == "" {
			return "chgrp sem group"
		}
	}
	return ""
}

// dsChmodArgs: "chmod [-R|-r] MODE PATH…" → recursão + modo + alvos.
// MODE é octal (644/0755) ou simbólico simples (u+x, ug=rwx,o=r).
func dsChmodArgs(t []string) (rec bool, mode string, paths []string, ok bool) {
	i := 1
	for ; i < len(t); i++ {
		a := t[i]
		if !strings.HasPrefix(a, "-") {
			break
		}
		switch a {
		case "-R", "-r", "--recursive", "-v":
		case "--changes", "-c":
		default:
			return false, "", nil, false // --reference/-f?/-m etc não mapeiam
		}
		rec = rec || a == "-R" || a == "-r" || a == "--recursive"
	}
	if i >= len(t) {
		return false, "", nil, false
	}
	mode = t[i]
	if !dsModeLit(mode) {
		return false, "", nil, false
	}
	for _, p := range t[i+1:] {
		if !dsPathLit(p) {
			return false, "", nil, false
		}
		paths = append(paths, p)
	}
	return rec, mode, paths, len(paths) > 0
}

// dsModeLit: octal de 3-4 dígitos ou simbólico "[ugoa]*[+-=][rwxXst]+(,…)*".
func dsModeLit(s string) bool {
	if _, ok := brParseOctal(s); ok {
		return true
	}
	if len(s) > 40 {
		return false
	}
	for _, clause := range strings.Split(s, ",") {
		op := strings.IndexAny(clause, "+-=")
		if op <= 0 {
			return false
		}
		for _, c := range clause[:op] {
			if c != 'u' && c != 'g' && c != 'o' && c != 'a' {
				return false
			}
		}
		rest := clause[op+1:]
		if rest == "" {
			return false
		}
		for _, c := range rest {
			if c != 'r' && c != 'w' && c != 'x' && c != 'X' && c != 's' && c != 't' {
				return false
			}
		}
	}
	return true
}

// dsChownArgs: "chown [-R] OWNER[:GROUP] PATH…".
func dsChownArgs(t []string) (rec bool, owner, group string, paths []string, ok bool) {
	i := 1
	for ; i < len(t); i++ {
		a := t[i]
		if !strings.HasPrefix(a, "-") {
			break
		}
		switch a {
		case "-R", "-r", "--recursive", "-v", "-c", "--changes":
		default:
			return false, "", "", nil, false // --from/--reference não mapeiam
		}
		rec = rec || a == "-R" || a == "-r" || a == "--recursive"
	}
	if i >= len(t) {
		return false, "", "", nil, false
	}
	spec := t[i]
	if strings.Contains(spec, ":") {
		parts := strings.SplitN(spec, ":", 2)
		owner, group = parts[0], parts[1]
	} else {
		owner = spec
	}
	if owner == "" && group == "" {
		return false, "", "", nil, false
	}
	for _, c := range owner + group {
		if !(c >= 'a' && c <= 'z') && !(c >= 'A' && c <= 'Z') && !(c >= '0' && c <= '9') &&
			c != '_' && c != '-' && c != '.' {
			return false, "", "", nil, false
		}
	}
	for _, p := range t[i+1:] {
		if !dsPathLit(p) {
			return false, "", "", nil, false
		}
		paths = append(paths, p)
	}
	return rec, owner, group, paths, len(paths) > 0
}

// dsPermFlag: um predicado de permissão/dono do find vira a chave do
// perm_find; devolve "" quando a forma não tem predicado equivalente
// (máscara simbólica fora de o+w/*+x, -perm /qualquer-um, etc). adv = 1
// quando o predicado carrega um argumento (o "644" do -perm 644).
func dsPermFlag(t []string, i int) (key string, adv int) {
	a := t[i]
	switch a {
	case "-nouser":
		return "no_owner", 0
	case "-nogroup":
		return "no_group", 0
	case "-user":
		return "owner", 1
	case "-group":
		return "group", 1
	case "-perm":
		if i+1 >= len(t) {
			return "", 0
		}
		v := t[i+1]
		if nuOctalShaped(v) {
			return "mode", 1
		}
		// "-perm -o+w" = world_writable; "-perm -u+x" ⊆ "algum bit exec"
		if v == "-o+w" {
			return "world_writable", 1
		}
		if strings.HasSuffix(v, "+x") && len(v) >= 3 && v[0] == '-' {
			return "executable", 1
		}
	}
	return "", 0
}

// dsFindSkips: dirs que o file_find já pula na travessia (espelha SKIP_DIRS
// do core.py + o conjunto local do script) — podar o que já se pula é neutro.
func dsFindSkips(name string) bool {
	name = strings.TrimSuffix(name, "/")
	switch name {
	case ".git", ".hg", ".svn", ".tox", ".venv", ".cache",
		"node_modules", "vendor", "dist", "build", "target",
		"__pycache__", "venv", "env", "coverage", "third_party", "Pods":
		return true
	}
	return false
}

// dsGlobAlt: globs de -name viram uma regex de basename para name_re —
// "*.{log,bak}" já chega separado pelo find; cada glob vira um ramo
// ancorado: "*.log" → ".*\.log$". Rejeita globs com classes/colchetes
// raros que mudariam a leitura.
func dsGlobAlt(globs []string) (string, bool) {
	if len(globs) > 8 {
		return "", false
	}
	arms := make([]string, 0, len(globs))
	for _, g := range globs {
		var b strings.Builder
		b.WriteString(".*")
		for _, r := range g {
			switch r {
			case '*':
				b.WriteString(".*")
			case '?':
				b.WriteByte('.')
			case '.', '+', '(', ')', '|', '^', '$', '[', ']', '{', '}', '\\':
				b.WriteByte('\\')
				b.WriteRune(r)
			default:
				b.WriteRune(r)
			}
		}
		b.WriteByte('$')
		arms = append(arms, b.String())
	}
	return "(?:" + strings.Join(arms, "|") + ")", true
}

// dsFindSize: "-size +12k" → bytes do filtro; unidades do find (c=1, w=2,
// k=1024, M, G; b e sem sufixo = blocos de 512). Só as formas relativas
// (+N / -N) viram min_size/max_size — o tamanho exato é semântica de
// arredondamento do find que o file_find não tem.
func dsFindSize(v string) (int64, byte, bool) {
	if v == "" || (v[0] != '+' && v[0] != '-') {
		return 0, 0, false
	}
	sign, s := v[0], v[1:]
	unit := int64(512)
	if len(s) > 0 {
		switch s[len(s)-1] {
		case 'c':
			unit, s = 1, s[:len(s)-1]
		case 'w':
			unit, s = 2, s[:len(s)-1]
		case 'k':
			unit, s = 1<<10, s[:len(s)-1]
		case 'M':
			unit, s = 1<<20, s[:len(s)-1]
		case 'G':
			unit, s = 1<<30, s[:len(s)-1]
		case 'b':
			s = s[:len(s)-1]
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0, 0, false
	}
	return n * unit, sign, true
}

// dsCapCand embrulha um método de caps (esqueleto ou generalizado) na
// candidata: intenção learned_<id>, atalho exato do texto do plano.
func dsCapCand(p pcPlan, m *mhMethod, tag string) *dsCand {
	c := &dsCand{Plan: p, Method: *m}
	name := dsLearnedPrefix + p.ID
	c.Method.Name = name + tag
	c.Method.Intent = name
	c.Intent = dsIntent{Name: name, Desc: oneLine(dsCleanTask(p.Task), 80), Examples: dsVariantTexts(p)}
	c.Alias = nuAlias{Text: dsCleanTask(p.Task), Intent: name, Ts: p.Time}
	return c
}

// dsCleanTask tira o sufixo de estilo que a colheita podia colar no texto
// da tarefa ("[estilo: ...]"): o atalho e o exemplo têm que ser o pedido
// que o usuário digitaria, não a instrução do professor.
var dsHintSuffix = lazyRx(`\s*\[(?:estilo|style)[^\]]*\]\s*$`)

func dsCleanTask(t string) string {
	return strings.TrimSpace(dsHintSuffix.ReplaceAllString(t, ""))
}

// dsVariantTexts: os textos que o plano já viu dar certo — o pedido e as
// testemunhas que fundiram no mesmo shape. Cada um vira exemplo (classifi-
// cador) e atalho (alias) na intenção aprendida: cada variante conhecida
// aumenta a cobertura da pedra sem custo de modelo.
func dsVariantTexts(p pcPlan) []string {
	var out []string
	seen := map[string]bool{}
	add := func(t string) {
		if x, ok := nuExampleText(dsCleanTask(t)); ok && !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	add(p.Task)
	for _, w := range p.Witnesses {
		add(w.Task)
	}
	return out
}

// dsEligible barra um plano antes do desenho do método: repetido,
// trajetória de bash registrada, tudo dentro das barras de comando e de
// replay. "" = elegível.
func dsEligible(p pcPlan) string {
	if p.Wins < dsMinWins {
		return fmt.Sprintf("só %d sucesso(s) verificado(s) — precisa ≥%d", max(p.Wins, 0), dsMinWins)
	}
	if len(p.Cmds) == 0 {
		return "sem comandos gravados (plano anterior à ponte)"
	}
	if len(p.Cmds) > dsMaxSteps {
		return "trajetória longa demais pra destilar"
	}
	for i, c := range p.Cmds {
		if why := dsCmdOK(c); why != "" {
			return fmt.Sprintf("comando %d não destilável: %s", i+1, why)
		}
		if !dsReplaySafe(c) {
			return fmt.Sprintf("comando %d muta estado, não dá pra re-verificar: %s", i+1, oneLine(c, 60))
		}
	}
	if v := dsVerifyCmd(p); v != "" {
		if why := dsCmdOK(v); why != "" {
			return "verificação não destilável: " + why
		}
		if !dsReplaySafe(v) {
			return "verificação não reproduzível sem mutar"
		}
	}
	return ""
}

// dsVerifyCmd é a verificação registrada do plano, ou "" quando ela é
// trivial/ausente (until "true" ou sem comando): aí a prova é o exit do
// próprio último passo — que já está na sequência.
func dsVerifyCmd(p pcPlan) string {
	v := strings.TrimSpace(p.Verify)
	if v == "" || v == "true" {
		return ""
	}
	return v
}

// ---------- destilação com slots (anti-unificação) ----------

// dsSlotRefRe casa os placeholders de um comando aprendido: $slot.<nome>.
var dsSlotRefRe = lazyRx(`\$slot\.([a-z][a-z0-9_]*)`)

// dsSlotNames lista os placeholders distintos de um template, na ordem.
func dsSlotNames(cmd string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range dsSlotRefRe.FindAllStringSubmatch(cmd, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}

// dsSlotInCmdWord: um placeholder nunca pode ser a PALAVRA de comando de
// nenhum segmento — "$slot.c -rf x" deixaria o valor escolher o binário.
func dsSlotInCmdWord(cmd string) bool {
	for _, seg := range reSplit.Split(cmd, -1) {
		f := shellFields(strings.TrimSpace(seg))
		if len(f) > 0 && dsSlotRefRe.MatchString(f[0]) {
			return true
		}
	}
	return false
}

// dsSlotPathKind: o slot nomeia um caminho do projeto — file/dir/test —
// ou o tipo é desconhecido (desconhecido confina, nunca abre).
func dsSlotPathKind(kinds []string) bool {
	if len(kinds) == 0 {
		return true
	}
	for _, k := range kinds {
		switch k {
		case "file", "dir", "test":
			return true
		}
	}
	return false
}

// dsSlotValOK valida um valor de slot na execução. Caminho fica confinado
// no root do projeto: nada de "..", absoluto ou "~".
func dsSlotValOK(path bool, v string) string {
	switch {
	case v == "":
		return "vazio"
	case strings.ContainsAny(v, "\n\r\x00"):
		return "byte de controle"
	case !path:
		return ""
	}
	switch {
	case v == "..", strings.HasPrefix(v, "../"), strings.Contains(v, "/../"), strings.HasSuffix(v, "/.."):
		return "caminho escapa o projeto (..)"
	case filepath.IsAbs(v) || strings.HasPrefix(v, `\`) || (len(v) > 1 && v[1] == ':'):
		return "caminho absoluto"
	case v == "~" || strings.HasPrefix(v, "~/"):
		return "caminho do HOME (~)"
	}
	return ""
}

// dsExpand preenche os $slot.x de um template com os valores do turno,
// validando cada um contra o tipo declarado. "" no segundo retorno = ok.
func dsExpand(tmpl string, slots map[string]string, kindOf func(string) []string) (string, string) {
	fail := ""
	out := dsSlotRefRe.ReplaceAllStringFunc(tmpl, func(ph string) string {
		if fail != "" {
			return ph
		}
		name := ph[len("$slot."):]
		v := slots[name]
		if why := dsSlotValOK(dsSlotPathKind(kindOf(name)), v); why != "" {
			fail = "$slot." + name + ": " + why
			return ph
		}
		return v
	})
	if fail != "" {
		return "", fail
	}
	return out, ""
}

// dsExpandBound preenche com os valores gravados da testemunha base (o
// replay do portão — literais que já saíram exit 0, sem confinamento).
func dsExpandBound(tmpl string, bind map[string]string) (string, bool) {
	ok := true
	out := dsSlotRefRe.ReplaceAllStringFunc(tmpl, func(ph string) string {
		v, has := bind[ph[len("$slot."):]]
		if !has {
			ok = false
			return ph
		}
		return v
	})
	return out, ok
}

// dsPreHas: o método exige slot.<nome> preenchido antes de rodar?
func dsPreHas(pre []mhCond, fact string) bool {
	for _, c := range pre {
		if c.Fact == fact && (c.Op == "" || c.Op == "set") {
			return true
		}
	}
	return false
}

// dsEntVal é o valor que a entidade j da variante vi carrega no slot —
// a mesma regra do fill (nlu.go): file/dir/test guarda o que foi digitado.
func dsEntVal(e pcEnt) string {
	switch e.Kind {
	case "file", "dir", "test":
		if e.Raw != "" {
			return e.Raw
		}
	}
	if e.Name != "" {
		return e.Name
	}
	return e.Raw
}

// dsAntiUnify: as testemunhas do MESMO Shape provam slots — mesma
// quantidade de comandos, mesmos tipos de entidade em cada posição, e o
// valor da entidade é a única coisa que muda de uma corrida pra outra.
// A prova é exata, por comando: substituir o valor do plano base por
// $slot.x e repreencher com os valores de CADA testemunha reconstrói o
// comando dela byte a byte — e o resultado passa na mesma barra
// (dsCmdOK + dsReplaySafe). Sem prova, o comando fica literal.
// Retorna templates por índice de comando, os slots provados (nome→tipo)
// e os valores do plano base pra replay (bind).
func dsAntiUnify(p pcPlan) (map[int]string, map[string]string, map[string]string) {
	if len(p.Ents) == 0 || len(p.Witnesses) == 0 {
		return nil, nil, nil
	}
	vars := []pcWitness{{Task: p.Task, Ents: p.Ents, Cmds: p.Cmds}}
	for _, wt := range p.Witnesses {
		if len(wt.Cmds) != len(p.Cmds) || len(wt.Ents) != len(p.Ents) {
			continue
		}
		ok := true
		for j := range wt.Ents {
			if wt.Ents[j].Kind != p.Ents[j].Kind {
				ok = false
				break
			}
		}
		if ok {
			vars = append(vars, wt)
		}
	}
	if len(vars) < 2 {
		return nil, nil, nil
	}
	jname := map[int]string{} // posição da entidade → nome do slot
	jof := map[string]int{}
	kinds := map[string]string{}
	cnt := map[string]int{}
	for j, e := range p.Ents {
		cnt[e.Kind]++
		n := e.Kind
		if cnt[e.Kind] > 1 {
			n = fmt.Sprintf("%s%d", e.Kind, cnt[e.Kind])
		}
		jname[j], jof[n], kinds[n] = n, j, e.Kind
	}
	tmpl := map[int]string{}
	used := map[string]bool{}
	for i, base := range p.Cmds {
		var reps [][2]string // [slot, valor da base] — maior primeiro: "a/b" antes de "b"
		for j := range p.Ents {
			if v0 := dsEntVal(vars[0].Ents[j]); v0 != "" && strings.Contains(base, v0) {
				reps = append(reps, [2]string{jname[j], v0})
			}
		}
		if len(reps) == 0 {
			continue
		}
		sort.Slice(reps, func(a, b int) bool { return len(reps[a][1]) > len(reps[b][1]) })
		t := base
		for _, r := range reps {
			t = strings.ReplaceAll(t, r[1], "$slot."+r[0])
		}
		if dsSlotInCmdWord(t) {
			continue
		}
		ok := true
		for vi := 1; vi < len(vars) && ok; vi++ {
			conc := t
			for _, r := range reps {
				conc = strings.ReplaceAll(conc, "$slot."+r[0], dsEntVal(vars[vi].Ents[jof[r[0]]]))
			}
			if conc != vars[vi].Cmds[i] || dsCmdOK(conc) != "" || !dsReplaySafe(conc) {
				ok = false
			}
		}
		if !ok {
			continue
		}
		tmpl[i] = t
		for _, r := range reps {
			used[r[0]] = true
		}
	}
	if len(tmpl) == 0 {
		return nil, nil, nil
	}
	slots, bind := map[string]string{}, map[string]string{}
	for n := range used {
		slots[n] = kinds[n]
		bind[n] = dsEntVal(vars[0].Ents[jof[n]])
	}
	return tmpl, slots, bind
}

// dsSlotsFitSchema: grudando numa intenção existente, cada placeholder só
// pode mapear num slot do schema dela, com tipo compatível. Sem encaixe,
// o método fica literal — nunca inventa placeholder incompatível.
func dsSlotsFitSchema(intent string, slots map[string]string) bool {
	m := nuGet()
	if m == nil || m.loadErr != nil {
		return false
	}
	di, ok := m.byName[intent]
	if !ok {
		return false
	}
	for n, k := range slots {
		sd, ok := m.data.Intents[di].Slots[n]
		if !ok {
			return false
		}
		ks := map[string]bool{}
		for _, x := range sd.Kinds {
			ks[x] = true
		}
		if !nuKindAllowed(ks, k) {
			return false
		}
	}
	return true
}

// dsBuild desenha o método de um plano elegível: cada comando vira um
// passo learned_cmd, o verificador vira check exit_zero no último passo,
// o texto do plano vira o atalho exato e (quando a gramática não tem onde
// encaixá-lo) a intenção learned_<id>. Quando o texto já lê numa intenção
// existente SEM método, o método gruda nela — a ponte tapa o buraco em
// vez de inventar outra palavra.
func dsBuild(p pcPlan, lib map[string][]mhMethod, w *nuWorld, root string) (*dsCand, string) {
	if why := dsEligible(p); why != "" {
		// skeleton distillation: a mutating plan can't replay literal
		// commands, but "find → per-item delete" still becomes a method of
		// VERIFIED caps — the online run taught the structure, not the bash.
		if strings.Contains(why, "muta estado") {
			if sk := dsSkeleton(p); sk != nil {
				return dsCapCand(p, sk, ".skeleton"), ""
			}
		}
		// cap-plan: generalização do esqueleto — cada mutação que cai numa
		// cap verificada vira passo dela; se tudo encaixar, o plano vira
		// método de caps em vez de sair do jogo.
		if cp := dsCapPlan(p, root); cp != nil {
			return dsCapCand(p, cp, ".caps"), ""
		}
		return nil, why
	}
	c := &dsCand{Plan: p}
	name := ""
	if in := nuGet().Parse(p.Task, w); in.Understood() && in.Name != "none" &&
		!fgIsMutating(in.Name) && !strings.HasPrefix(in.Name, dsLearnedPrefix) && len(lib[in.Name]) == 0 {
		c.Attach = in.Name // buraco conhecido: a intenção existe e não tem método
		name = in.Name
	} else {
		name = dsLearnedPrefix + p.ID
		c.Intent = dsIntent{Name: name, Desc: oneLine(dsCleanTask(p.Task), 80), Examples: dsVariantTexts(p)}
	}
	c.Alias = nuAlias{Text: dsCleanTask(p.Task), Intent: name, Ts: p.Time}
	// anti-unificação: testemunhas do mesmo Shape provam os $slot.x; numa
	// intenção existente eles ainda precisam mapear no schema dela.
	tmpl, slots, bind := dsAntiUnify(p)
	if len(slots) > 0 && c.Attach != "" && !dsSlotsFitSchema(c.Attach, slots) {
		tmpl, slots, bind = nil, nil, nil
	}
	var steps []mhStep
	last := ""
	for i, cmd := range p.Cmds {
		if t := tmpl[i]; t != "" {
			cmd = t
		}
		last = fmt.Sprintf("r%d", i+1)
		steps = append(steps, mhStep{Cap: "learned_cmd", In: map[string]string{"cmd": cmd}, Out: last})
	}
	if v := dsVerifyCmd(p); v != "" && p.Cmds[len(p.Cmds)-1] != v {
		last = fmt.Sprintf("r%d", len(steps)+1)
		steps = append(steps, mhStep{Cap: "learned_cmd", In: map[string]string{"cmd": v}, Out: last})
	}
	var pre []mhCond
	var mslots map[string]string
	if len(slots) > 0 {
		mslots = map[string]string{}
		c.Bind = bind
		var names []string
		for n := range slots {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			mslots[n] = slots[n]
			pre = append(pre, mhCond{Fact: "slot." + n, Op: "set"})
			if c.Attach == "" {
				if c.Intent.Slots == nil {
					c.Intent.Slots = map[string]nuSlotDef{}
				}
				c.Intent.Slots[n] = nuSlotDef{Kinds: []string{slots[n]}, Required: true}
			}
		}
	}
	risk := "low"
	for _, cm := range p.Cmds {
		if !IsReadOnly(cm) {
			risk = "med"
			break
		}
	}
	c.Method = mhMethod{
		Name: name + ".replay", Intent: name, Priority: 0,
		Doc:    "aprendido (jazz distill): " + oneLine(p.Task, 90),
		Pre:    pre,
		Steps:  steps,
		Verify: []mhCheck{{Check: "exit_zero", Arg: "$" + last}},
		Slots:  mslots,
		Risk:   risk, CostMS: len(steps) * 2000,
	}
	return c, ""
}

// ---------- o portão ----------

// dsRunner roda um comando do replay e devolve o exit code (injetável nos
// testes; em produção é o Shell do projeto).
type dsRunner func(ctx context.Context, cmd string, timeout time.Duration) int

// dsGateRep é o relatório auditável de uma candidata.
type dsGateRep struct {
	Plan     string      `json:"plan"`
	Task     string      `json:"task"`
	Intent   string      `json:"intent"`
	Method   string      `json:"method"`
	Accept   bool        `json:"accept"`
	Reason   string      `json:"reason"`
	Replay   []dsStepRep `json:"replay,omitempty"`
	NLU      *GateReport `json:"nlu,omitempty"`
	Directed bool        `json:"directed,omitempty"`
}

type dsStepRep struct {
	Cmd  string `json:"cmd"`
	Exit int    `json:"exit"`
}

// dsGate é o portão: primeiro o replay literal (a sequência verificada
// inteira sai exit 0 de novo — é a prova de benefício), depois, quando a
// candidata cria intenção nova, o held-out da gramática (mesma disciplina
// da forja: NonInferior, âncora de release, McNemar por intenção, guarda
// estrita de negativos e prompts de eval) e o teste dirigido.
func dsGate(ctx context.Context, root string, c *dsCand, run dsRunner, seed int64, mov *dsOverlay) dsGateRep {
	rep := dsGateRep{Plan: c.Plan.ID, Task: oneLine(c.Plan.Task, 120), Method: c.Method.Name}
	if c.Attach != "" {
		rep.Intent = c.Attach
	} else {
		rep.Intent = c.Intent.Name
	}
	// (a) replay — cada passo sai com exit 0 no projeto de verdade. Um
	// template com $slot.x roda com os valores do plano base (a corrida
	// verificada que gerou o template — a prova já é dela).
	for i, st := range c.Method.Steps {
		cmd := st.In["cmd"]
		if strings.Contains(cmd, "$slot.") {
			if x, ok := dsExpandBound(cmd, c.Bind); ok {
				cmd = x
			}
		}
		code := run(ctx, cmd, dsCmdTimeout)
		rep.Replay = append(rep.Replay, dsStepRep{Cmd: cmd, Exit: code})
		if code != 0 {
			rep.Reason = fmt.Sprintf("replay: passo %d saiu com exit %d — a trajetória não re-verifica", i+1, code)
			return rep
		}
	}
	rep.Directed = true // replay é o benefício dirigido do método
	// (b) intenção nova passa pelo portão da gramática.
	if c.Attach == "" {
		ok, gr, why := dsNLUGate(c, seed, mov)
		rep.NLU = &gr
		if !ok {
			rep.Reason = why
			return rep
		}
	}
	rep.Accept = true
	rep.Reason = "aceito: replay verde e " + map[bool]string{true: "sem regressão no portão", false: "método numa intenção existente"}[c.Attach == ""]
	return rep
}

// dsNLUGate prova que a intenção aprendida nova não regride a gramática:
// mesmo conjunto pontuado da forja (exemplos embutidos + negativos +
// prompts de eval), mesmo split por chave da instalação, mesmas réguas.
func dsNLUGate(c *dsCand, seed int64, mov *dsOverlay) (bool, GateReport, string) {
	lx, _ := nuFS.ReadFile("nlu/lexicon.json")
	ds, _ := nuFS.ReadFile("nlu/intents.json")
	cur, _ := nuReadOverlay(nuOverlayPath())
	base := dsModel(lx, ds, cur, mov, nil, nil, nil)
	if base.loadErr != nil {
		return false, GateReport{}, "modelo base não compila: " + base.loadErr.Error()
	}
	w := nuWorldFromDef(base.data.World)
	var exs []Example
	keyOf := map[string]string{}
	for _, it := range base.data.Intents {
		for _, e := range it.Examples {
			cn := base.canonOf(e)
			id := "g:" + it.Name + ":" + cn
			exs = append(exs, Example{ID: id, Input: e, Want: it.Name, Group: "g:" + it.Name})
			keyOf[id] = it.Name + "\x00" + cn
		}
		for _, n := range it.Negatives {
			exs = append(exs, Example{ID: "n:" + it.Name + ":" + base.canonOf(n), Input: n, Want: "!" + it.Name, Group: "neg"})
		}
	}
	exs = append(exs, fgEvalPrompts()...)
	inEx, outEx := SplitKeyed(exs, fgHeldOut, fgSplitKey(fgOpts{Seed: seed}))
	outKeys := map[string]bool{}
	for _, ex := range outEx {
		if k, ok := keyOf[ex.ID]; ok {
			outKeys[k] = true
		}
	}
	// âncora de release: a gramática embarcada sozinha, sem nenhum overlay.
	anchorM := dsModel(lx, ds, nil, nil, nil, nil, outKeys)
	av, bv, cv := map[string]fgVerdict{}, map[string]fgVerdict{}, map[string]fgVerdict{}
	anchorIn := fgScore(anchorM, w, av)(inEx)
	anchorOut := fgScore(anchorM, w, av)(outEx)
	seqBaseM := dsModel(lx, ds, cur, mov, nil, nil, outKeys)
	candM := dsModel(lx, ds, cur, mov, &c.Intent, &c.Alias, outKeys)
	bIn := fgScore(seqBaseM, w, bv)(inEx)
	bOut := fgScore(seqBaseM, w, bv)(outEx)
	cIn := fgScore(candM, w, cv)(inEx)
	cOut := fgScore(candM, w, cv)(outEx)
	gr := NonInferior(bIn, bOut, cIn, cOut, len(inEx), len(outEx))
	if !gr.Accept {
		return false, gr, gr.Reason
	}
	if ga := NonInferior(anchorIn, anchorOut, cIn, cOut, len(inEx), len(outEx)); !ga.Accept {
		return false, gr, "vs release: " + ga.Reason
	}
	// McNemar por intenção no held-out + zero regressão em mutante.
	perIntent := map[string]*[2]int{}
	for _, ex := range outEx {
		b, cc := bv[ex.ID], cv[ex.ID]
		var want string
		if strings.HasPrefix(ex.Group, "g:") {
			want = strings.TrimPrefix(ex.Group, "g:")
			if b.under == cc.under {
				continue
			}
		} else if ex.Group == "eval" {
			want = ex.Want
			if b.acts == cc.acts {
				continue
			}
		} else {
			continue
		}
		pr := perIntent[want]
		if pr == nil {
			pr = &[2]int{}
			perIntent[want] = pr
		}
		if (ex.Group == "eval" && b.acts) || (ex.Group != "eval" && b.under) {
			pr[0]++
		} else {
			pr[1]++
		}
	}
	for name, pr := range perIntent {
		if pr[0] > 0 && fgIsMutating(name) {
			return false, gr, fmt.Sprintf("regressão em intenção mutante %s: %d exemplo(s) held-out pioraram", name, pr[0])
		}
		if p := McNemarExact(pr[0], pr[1]); p < fgMcNemar {
			return false, gr, fmt.Sprintf("regressão significativa em %s (McNemar p=%.3f, %d↓%d↑)", name, p, pr[0], pr[1])
		}
	}
	// guarda estrita: negativo que vira acerto ou prompt de eval que regride.
	var regressed []string
	for _, ex := range exs {
		b, cc := bv[ex.ID], cv[ex.ID]
		if ex.Group == "neg" && !b.falseSucc && cc.falseSucc || ex.Group == "eval" && b.acts && !cc.acts {
			regressed = append(regressed, ex.ID)
		}
	}
	if len(regressed) > 0 {
		return false, gr, fmt.Sprintf("%d caso(s) de guarda regrediram (%s)", len(regressed), strings.Join(regressed[:min(3, len(regressed))], ", "))
	}
	// dirigido: o texto do plano lê na intenção nova e age.
	if in := candM.Parse(c.Alias.Text, w); !(fgActs(in) && in.Name == c.Intent.Name) {
		return false, gr, "teste dirigido: o texto do plano não lê na intenção aprendida"
	}
	return true, gr, ""
}

// dsModel compila um modelo como fgModel, mais o overlay de métodos e a
// candidata (intenção + atalho) opcionais — treinado sem as chaves held-out.
func dsModel(lx, ds []byte, cur *nuOverlay, mov *dsOverlay, addI *dsIntent, addA *nuAlias, out map[string]bool) *nuModel {
	m := nuCompile(lx, ds)
	if m.loadErr != nil {
		return m
	}
	nuApplyOverlay(m, cur)
	dsApplyNLU(m, mov)
	if addI != nil {
		dsAddIntent(m, *addI)
	}
	if addA != nil && addA.Intent != "" {
		if canon := m.canonOf(addA.Text); canon != "" {
			if _, ok := m.byName[addA.Intent]; ok {
				if m.aliases == nil {
					m.aliases = map[string]string{}
				}
				m.aliases[canon] = addA.Intent
			}
		}
	}
	m.nb = m.nuTrain(func(ii, ei int) bool {
		return len(out) > 0 && out[m.data.Intents[ii].Name+"\x00"+m.canonOf(m.data.Intents[ii].Examples[ei])]
	})
	m.nbOnce.Do(func() {})
	return m
}

// ---------- jazz distill ----------

type dsOpts struct {
	DryRun  bool
	JSON    bool
	Overlay string // "" = dsOverlayPath()
	Seed    int64  // != 0: split determinístico pra testes
}

type dsReport struct {
	Accept  bool        `json:"accept"`
	Reason  string      `json:"reason"`
	DryRun  bool        `json:"dry_run"`
	Overlay string      `json:"overlay"`
	Wrote   bool        `json:"wrote"`
	Plans   int         `json:"plans"`
	Added   []dsGateRep `json:"added,omitempty"`
	Dropped []dsDropped `json:"dropped,omitempty"`
}

type dsDropped struct {
	Plan   string `json:"plan"`
	Task   string `json:"task"`
	Reason string `json:"reason"`
}

// cmdDistill implementa `jazz distill`.
func cmdDistill(root string, args []string, w io.Writer) int {
	o := dsOpts{Overlay: dsOverlayPath()}
	var list bool
	var forget string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--dry-run", "-n":
			o.DryRun = true
		case "--json":
			o.JSON = true
		case "--list", "-l":
			list = true
		case "--forget":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "uso: jazz distill --forget <plano|método|intenção>")
				return 2
			}
			i++
			forget = args[i]
		default:
			fmt.Fprintln(os.Stderr, "uso: jazz distill [--dry-run] [--json] [--list] [--forget X]")
			return 2
		}
	}
	if forget != "" {
		return dsForget(o, forget, w)
	}
	if list {
		return dsList(root, o, w)
	}
	rep, err := dsRun(context.Background(), root, o)
	if err != nil {
		fmt.Fprintln(w, "destilação:", err)
		return 1
	}
	if o.JSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", " ")
		enc.Encode(rep)
	} else {
		dsPrint(w, rep)
	}
	if !rep.Accept {
		return 1
	}
	return 0
}

// dsList mostra as candidatas e o que já foi aprendido.
func dsList(root string, o dsOpts, w io.Writer) int {
	lib, _ := mhLibrary()
	wr := nuRuntimeWorld(root, epGet(false))
	plans := pcLoad(root)
	ov, _ := dsReadOverlay(o.Overlay)
	learned := map[string]bool{}
	if ov != nil {
		for _, p := range ov.Prov {
			learned[p.Plan] = true
		}
	}
	fmt.Fprintf(w, "destilação: %d plano(s) verificado(s) no cache\n", len(plans))
	for _, p := range plans {
		short := p.ID
		if len(short) > 6 {
			short = short[:6]
		}
		if learned[p.ID] {
			fmt.Fprintf(w, "  ✓ %s %q — já aprendido (%d verificações)\n", short, oneLine(p.Task, 60), p.Wins)
			continue
		}
		c, why := dsBuild(p, lib, wr, root)
		if c == nil {
			fmt.Fprintf(w, "  - %s %q: %s\n", short, oneLine(p.Task, 60), why)
			continue
		}
		fmt.Fprintf(w, "  + %s %q → %s (%d passos, risco %s)\n", short, oneLine(p.Task, 60), c.Method.Intent, len(c.Method.Steps), c.Method.Risk)
	}
	if ov != nil && len(ov.Methods) > 0 {
		fmt.Fprintf(w, "aprendidos (%d):\n", len(ov.Methods))
		for _, m := range ov.Methods {
			fmt.Fprintf(w, "  %s → %s (%d passos)\n", m.Name, m.Intent, len(m.Steps))
		}
	}
	return 0
}

// dsRun é a fábrica inteira: colhe candidatas, porta, promove, grava.
func dsRun(ctx context.Context, root string, o dsOpts) (*dsReport, error) {
	rep := &dsReport{DryRun: o.DryRun, Overlay: o.Overlay}
	plans := pcLoad(root)
	rep.Plans = len(plans)
	lib, _ := mhLibrary()
	wr := nuRuntimeWorld(root, epGet(false))
	ov, err := dsReadOverlay(o.Overlay)
	if err != nil {
		return nil, fmt.Errorf("overlay %s inválido (%v): conserte ou apague antes de destilar", o.Overlay, err)
	}
	learned := map[string]bool{}
	if ov != nil {
		for _, p := range ov.Prov {
			learned[p.Plan] = true
		}
	}
	var cands []*dsCand
	dirty := false
	defer func() {
		if dirty && !o.DryRun {
			if err := pcSave(root, plans); err != nil {
				dbg("distill", map[string]any{"carimbo": "não gravou", "err": err.Error()})
			}
		}
	}()
	idxByID := map[string]int{}
	for i := range plans {
		idxByID[plans[i].ID] = i
		p := plans[i]
		if learned[p.ID] {
			continue
		}
		if p.DsDrop != "" && p.Wins <= p.DsWins && p.DsGen == dsCapGen {
			continue // mesmo veredito, mesma evidência, mesma geração de caps
		}
		c, why := dsBuild(p, lib, wr, root)
		if c == nil {
			if why != "" && p.Wins >= dsMinWins {
				rep.Dropped = append(rep.Dropped, dsDropped{Plan: p.ID, Task: oneLine(p.Task, 80), Reason: why})
				plans[i].DsDrop, plans[i].DsWins, plans[i].DsGen, dirty = why, p.Wins, dsCapGen, true
			}
			continue
		}
		cands = append(cands, c)
	}
	if len(cands) == 0 {
		rep.Reason = "nada a destilar: nenhum plano verificado e repetido sobrou pra promover"
		return rep, nil
	}
	// determinismo: mais verificado primeiro, id desempata.
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].Plan.Wins != cands[j].Plan.Wins {
			return cands[i].Plan.Wins > cands[j].Plan.Wins
		}
		return cands[i].Plan.ID < cands[j].Plan.ID
	})
	if len(cands) > dsMaxCand {
		for _, c := range cands[dsMaxCand:] {
			rep.Dropped = append(rep.Dropped, dsDropped{Plan: c.Plan.ID, Task: oneLine(c.Plan.Task, 80), Reason: "orçamento do portão por rodada — fica pra próxima"})
		}
		cands = cands[:dsMaxCand]
	}
	sh := NewShell(root)
	defer sh.Close()
	run := func(ctx context.Context, cmd string, timeout time.Duration) int {
		r := sh.Run(ctx, cmd, timeout, nil)
		return r.ExitCode
	}
	now := time.Now().Unix()
	for _, c := range cands {
		g := dsGate(ctx, root, c, run, o.Seed, ov)
		if !g.Accept {
			rep.Dropped = append(rep.Dropped, dsDropped{Plan: c.Plan.ID, Task: oneLine(c.Plan.Task, 80), Reason: g.Reason})
			if i, ok := idxByID[c.Plan.ID]; ok {
				plans[i].DsDrop, plans[i].DsWins, plans[i].DsGen, dirty = g.Reason, c.Plan.Wins, dsCapGen, true
			}
			continue
		}
		rep.Added = append(rep.Added, g)
		if o.DryRun {
			continue
		}
		if ov == nil {
			ov = &dsOverlay{Version: dsOverlayVersion, Aliases: map[string]nuAlias{}}
		}
		if ov.Aliases == nil {
			ov.Aliases = map[string]nuAlias{}
		}
		if c.Intent.Name != "" {
			ov.Intents = append(ov.Intents, c.Intent)
		}
		ov.Methods = append(ov.Methods, c.Method)
		// o atalho fica indexado pelo canon que a gramática realmente vê
		m := dsModelMustNoErr()
		if m != nil {
			canon := m.canonOf(c.Alias.Text)
			if canon != "" {
				ov.Aliases[canon] = c.Alias
			}
			// método parametrizado: a forma mascarada também atalha —
			// "comprime @dir" lê "comprime docs" e "comprime src" na mesma
			// intenção, com a entidade preenchendo o slot (nlu.go Parse).
			if len(c.Method.Slots) > 0 && c.Plan.Shape != "" && c.Plan.Shape != canon {
				ov.Aliases[c.Plan.Shape] = nuAlias{Text: c.Plan.Shape, Intent: c.Alias.Intent, Ts: c.Alias.Ts}
			}
			// cada variante vista dar certo (testemunhas do mesmo shape)
			// vira atalho também — canon literal, e a forma mascarada dela
			// quando há slot pra receber a entidade. Cobertura real sem
			// uma chamada de modelo a mais.
			for _, vt := range c.Intent.Examples {
				vc := m.canonOf(vt)
				if vc == "" || vc == canon {
					continue
				}
				if _, dup := ov.Aliases[vc]; !dup {
					ov.Aliases[vc] = nuAlias{Text: vt, Intent: c.Alias.Intent, Ts: c.Alias.Ts}
				}
				if len(c.Method.Slots) > 0 {
					if vs, _ := pcShapeEnts(vt, wr); vs != "" && vs != vc {
						if _, dup := ov.Aliases[vs]; !dup {
							ov.Aliases[vs] = nuAlias{Text: vs, Intent: c.Alias.Intent, Ts: c.Alias.Ts}
						}
					}
				}
			}
		}
		ov.Prov = append(ov.Prov, dsProv{Plan: c.Plan.ID, Task: oneLine(c.Plan.Task, 120), Intent: g.Intent, Method: c.Method.Name, Wins: c.Plan.Wins, Ts: now})
	}
	if len(rep.Added) == 0 {
		rep.Reason = "rejeitado: nenhum plano passou o portão"
		return rep, nil
	}
	rep.Accept = true
	rep.Reason = fmt.Sprintf("aceito: %d procedimento(s) virou método offline", len(rep.Added))
	if o.DryRun {
		return rep, nil
	}
	if err := dsWriteOverlay(o.Overlay, ov); err != nil {
		return nil, err
	}
	rep.Wrote = true
	auditRec("distill.applied", map[string]any{"path": o.Overlay, "added": len(rep.Added)})
	return rep, nil
}

// dsModelMustNoErr é o nuGet do processo pra calcular o canon do atalho;
// nil (sem gramática) não pode acontecer numa corrida que já destilou.
func dsModelMustNoErr() *nuModel {
	m := nuGet()
	if m == nil || m.loadErr != nil {
		return nil
	}
	return m
}

// dsForget remove uma intenção/método/atalho/proveniência aprendidos.
// A chave pode ser o nome do método, o nome da intenção ou (prefixo do) id
// do plano de origem. Com --dry-run mostra o que sairia, sem gravar.
func dsForget(o dsOpts, key string, w io.Writer) int {
	ov, err := dsReadOverlay(o.Overlay)
	if err != nil || ov == nil {
		fmt.Fprintln(w, "nada aprendido ainda")
		return 1
	}
	intent := ""
	resolve := func(v string) {
		if intent == "" && v != "" {
			intent = v
		}
	}
	for _, m := range ov.Methods {
		if m.Name == key || m.Intent == key {
			resolve(m.Intent)
		}
	}
	for _, it := range ov.Intents {
		if it.Name == key {
			resolve(it.Name)
		}
	}
	for _, p := range ov.Prov {
		if p.Plan == key || strings.HasPrefix(p.Plan, key) {
			resolve(p.Intent)
		}
	}
	for _, a := range ov.Aliases {
		if a.Intent == key || a.Text == key {
			resolve(a.Intent)
		}
	}
	if intent == "" {
		fmt.Fprintf(w, "não achei %q entre os aprendidos\n", key)
		return 1
	}
	var methods []mhMethod
	var dropM []string
	for _, m := range ov.Methods {
		if m.Intent == intent {
			dropM = append(dropM, m.Name)
			continue
		}
		methods = append(methods, m)
	}
	var intents []dsIntent
	for _, it := range ov.Intents {
		if it.Name != intent {
			intents = append(intents, it)
		}
	}
	var prov []dsProv
	for _, p := range ov.Prov {
		if p.Intent != intent {
			prov = append(prov, p)
		}
	}
	var aliases map[string]nuAlias
	for c, a := range ov.Aliases {
		if a.Intent != intent {
			if aliases == nil {
				aliases = map[string]nuAlias{}
			}
			aliases[c] = a
		}
	}
	if o.DryRun {
		fmt.Fprintf(w, "dry-run: sairia a intenção %s (%d método(s), %d atalho(s))\n", intent, len(dropM), len(ov.Aliases)-len(aliases))
		return 0
	}
	ov.Methods, ov.Intents, ov.Prov, ov.Aliases = methods, intents, prov, aliases
	if err := dsWriteOverlay(o.Overlay, ov); err != nil {
		fmt.Fprintln(w, "falhou gravando:", err)
		return 1
	}
	fmt.Fprintf(w, "esquecido: %s (%d método(s), intenção e atalho removidos)\n", intent, len(dropM))
	return 0
}

// dsPrint é o relatório humano.
func dsPrint(w io.Writer, r *dsReport) {
	fmt.Fprintf(w, "destilação: %d plano(s) no cache · %d promovido(s) · %d descartado(s)\n", r.Plans, len(r.Added), len(r.Dropped))
	for _, a := range r.Added {
		fmt.Fprintf(w, "  + %q → %s (%s, %d passos)\n", a.Task, a.Method, a.Intent, len(a.Replay))
		for _, st := range a.Replay {
			fmt.Fprintf(w, "      $ %s → exit %d\n", oneLine(st.Cmd, 70), st.Exit)
		}
		if a.NLU != nil {
			fmt.Fprintf(w, "      portão: %s\n", a.NLU.Reason)
		}
	}
	for _, d := range r.Dropped {
		fmt.Fprintf(w, "  - %q: %s\n", d.Task, d.Reason)
	}
	fmt.Fprintln(w, "portão:", r.Reason)
	switch {
	case r.Wrote:
		fmt.Fprintln(w, "gravado:", r.Overlay)
	case r.Accept && r.DryRun:
		fmt.Fprintln(w, "dry-run: nada gravado (seria:", r.Overlay+")")
	default:
		fmt.Fprintln(w, "nada gravado")
	}
}

// dsNote avisa uma vez, quando um plano cruza o limiar de repetição: "isso
// já dá pra virar método offline". Chamada de acTurnDone depois do pcStore.
func dsNote(a *Agent, root string, p pcPlan) {
	if !nuForgeOn() {
		return
	}
	for _, q := range pcLoad(root) {
		if pcJaccard(q.Keys, p.Keys) < 0.7 && (q.Shape == "" || q.Shape != p.Shape) {
			continue
		}
		if q.Wins == dsMinWins && dsEligible(q) == "" {
			a.ui.Notice(fmt.Sprintf("procedimento verificado %d× — 'jazz distill' promove a método offline", q.Wins))
		}
		return
	}
}

// dsLearnedIntent: a intenção é aprendida (prefixo reservado).
func dsIsLearned(name string) bool { return strings.HasPrefix(name, dsLearnedPrefix) }

// ---------- learned_cmd (a capacidade dos métodos aprendidos) ----------

// brLearnedCmd executa UM comando gravado por `jazz distill` — literal ou
// um template com $slot.x que as testemunhas provaram. A mesma cerca de
// qualquer comando do cérebro: env.Permit decide, o audit registra, o
// orçamento conta, e o shadow-git cobre os efeitos pra /undo. A validação
// roda de novo aqui — um overlay editado à mão tem a mesma barra da carga
// (nada perigoso, nada de segredo, uma linha) — e a substituição também:
// caminho confinado no projeto e o comando preenchido reproduzível.
func brLearnedCmd(cx *brCtx, in map[string]string) brStep {
	cmd := strings.TrimSpace(in["cmd"])
	refuse := func(why string) brStep {
		obs := rvNoCmd()
		obs.Output = "comando aprendido recusado: " + why
		return brStep{obs: obs, why: obs.Output}
	}
	if strings.Contains(cmd, "$slot.") {
		if dsSlotInCmdWord(cmd) { // nunca o binário: só argumentos
			return refuse("slot na palavra de comando")
		}
		ex, why := dsExpand(cmd, cx.task.Slots, func(n string) []string {
			if k, ok := cx.mslots[n]; ok {
				return []string{k}
			}
			return nil // tipo desconhecido: valor tratado como caminho (confinado)
		})
		if why != "" {
			return refuse(why)
		}
		cmd = ex
		if !dsReplaySafe(cmd) { // a substituição não pode mudar a natureza do comando
			return refuse("a substituição sai do reproduzível")
		}
	}
	if why := dsCmdOK(cmd); why != "" {
		return refuse(why)
	}
	out, exit, obs := cx.runCmd(cmd, dsCmdTimeout)
	if obs.Denied || obs.BudgetLeft == 0 {
		return brStep{obs: obs, why: obs.Output}
	}
	shown := strings.TrimRight(truncateHeadTail(out, 4000), "\n")
	if shown == "" {
		shown = "(sem saída)"
	}
	cx.say("$ %s — aprendido\n%s", cmd, shown)
	if exit != 0 {
		return brStep{val: brCmdRes{cmd, exit, out}, obs: obs, why: cmd + " falhou"}
	}
	return brStep{val: brCmdRes{cmd, exit, out}, obs: obs, ok: true}
}
