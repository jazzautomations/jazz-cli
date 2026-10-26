package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

var version = "0.8.1"

func usage() {
	fmt.Print(`jazz — agente de código no terminal

uso:
  jazz [prompt]                 abre o modo interativo (com prompt inicial opcional)
  jazz -p "prompt"              headless: roda até o fim e imprime só a resposta
  echo "prompt" | jazz -p       idem lendo do stdin
  jazz -p "x" - < arq          lê o stdin até o fim (ou --stdin); sem isso, o stdin só é lido se
                               chegar dado logo, e nunca segura a execução
  jazz -c                       continua a última sessão desta pasta (também com -p)
  jazz -r [id]                  retoma uma sessão: sem id abre a lista (↑↓, digite pra filtrar); com id também no -p
                               (<id>@<entrada> retoma num ponto da árvore)
  jazz -p --resume-last-in DIR  retoma a última sessão de DIR (ou começa uma nova); sem prompt, continua de onde parou

  jazz login [chatgpt] [--browser]   login com a conta do ChatGPT (código no celular)
  jazz key <provedor>                salva uma chave (openrouter, anthropic, openai, groq, zen)
  jazz logout <provedor>             apaga a credencial
  jazz provider add <nome> --url URL [--type openai] [--key-env VAR] [--model M]
                     [--header K=V] [--no-key] [--ollama]
                                     adiciona um provedor (Ollama, llama.cpp, LM Studio, qualquer URL OpenAI)
  jazz models [provedor]             lista modelos
  jazz models --scores               placar do juiz: acerto, tempo e 429 por modelo e tipo de tarefa
  jazz omni setup                    liga o roteamento entre todos os seus provedores (grátis primeiro)
  jazz export <id> [md|html] [arq]   exporta uma sessão desta pasta
  jazz trust [--yes] [--revoke]      confia na pasta e na configuração do projeto (AGENTS.md, skills, .mcp.json, hooks, regras allow)
  jazz deps audit [pasta]            verifica as dependências do projeto (existe? idade, typosquat, OSV)
  jazz sanitize <arquivo>            mostra o que seria removido (texto invisível, HTML escondido) antes de ir ao modelo
  jazz update [--check]              atualiza o jazz (verifica sha256)
  jazz bug                           gera um pacote .tar.gz pra reportar problema
  jazz doctor [-v] [--sandbox-probe] diagnóstico (-v: argv do sandbox; probe: o que o sandbox lê)
  jazz doctor --provider <p>[/modelo] testa um pedido real e mostra HTTP + erro (sem a chave)
  jazz script [nome] ['{json}']      lista os scripts prontos ou roda um (JSON no stdout, sem modelo)
  jazz sessions [--json] [-n N]      sessões desta pasta: situação (concluída/incompleta/negada), tokens, custo, duração
  jazz trace [id] [--json]           linha do tempo de uma sessão (requisições, ferramentas, bloqueios, checkpoints)
  jazz audit [--since 24h] [--verify] [--json]   log de auditoria encadeado por hash (e verificação da cadeia)
  jazz changes <arquivo>             quem mudou o arquivo: sessão, passo, modelo e prompt (alias: jazz blame)
  jazz improve [--apply] [-n N]      lições das últimas execuções: mostra as propostas (com evidência); --apply grava
  jazz improve --harness [--from D]  relatório de falhas recorrentes do harness em .jazz/improve-report.md (só proposta)
  jazz worktree list | merge <nome> [--yes] | rm <nome> [--force]
                                worktrees git criados com --worktree (mesclar com --no-ff, listar, apagar)
  jazz version

opções:
  -m <provedor/modelo>   modelo (ex.: anthropic/claude-sonnet-5-5, openrouter/openrouter/free)
  -C <pasta>             pasta do projeto (padrão: atual)
  --worktree[=nome]      roda a sessão num worktree git novo (.jazz/worktrees/<nome>, branch jazz/<nome>);
                         o checkout principal fica intocado; depois: jazz worktree merge <nome>
  --turbo | --yolo | --ask | --smart  modo de permissão só nesta sessão (padrão: auto — pede confirmação em comando perigoso;
                         turbo/--full-auto: com sandbox do sistema, só pergunta o realmente necessário)
  --ack-no-sandbox                  confirma YOLO headless sem sandbox do sistema; não cria isolamento
  -v                     (com -p) mostra o progresso no stderr
  --output-format F      (com -p) text (padrão) · json (só o resultado final) · stream-json (um evento por linha)
  --max-steps N          limite de passos do modelo por turno
  --timeout D            (com -p) prazo total da execução (padrão 10m; 0 = sem prazo; ex.: 90s, 30m)
  --timeout-grace D      (com -p) antes do prazo, para de pedir ao modelo, salva checkpoint e nota de passagem
                         (padrão 10% do --timeout, entre 5s e 60s)
  --metrics-out ARQ      (com -p) grava os totais da execução em JSON (tokens, custo, ferramentas, situação)
  --otlp-endpoint URL    exporta traces/métricas OTLP/HTTP JSON (ex.: http://localhost:4318); config otlp.endpoint; desligado por padrão
  --allow-tools L        libera sem perguntar: bash,edit,write,fetch ou regras como 'Bash(npm test:*)'
  --system-append T      texto extra no fim do prompt de sistema
  --until "CMD"          só termina quando CMD sai com 0 (roda no shell; falhou, o modelo continua)
  --until-rounds N       máximo de vezes que o --until roda por turno (padrão 5)
  --follow-up "MSG"      (com -p, repetível) roda MSG como turno seguinte quando o anterior termina bem;
                         JAZZ_STEER_FILE=ARQ: cada linha nova do arquivo entra no turno como mensagem "agora"
  --review               depois de um turno com mudanças, um subagente só-leitura revisa o diff
  --omni-judge on|off    escolhe o modelo de cada turno pelo tipo de tarefa e pelo placar medido
                         (UCB1 sobre os candidatos grátis; padrão off; config judge.mode; JAZZ_OMNI_JUDGE)
  --verifier on|off      antes de terminar, um segundo modelo revisa o diff contra o pedido (padrão off)
  --hedge on|off         pedidos pequenos (classificador, resumo, verificador) vão a 2 modelos; vale o
                         primeiro (padrão: ligado junto com --omni-judge)
  --panel on|off         passos difíceis sem ferramentas (plano de tarefa grande, causa raiz após 2 testes
                         falhos): 2–3 modelos em paralelo e uma síntese (padrão off)
  --fusion on|off        líder planeja e revisa; um modelo rápido (sidekick) implementa itens delegados
                         no próprio contexto (padrão off; config fusion.lead/sidekick)
  --parse-output[=false] resume saída longa de teste/build/stack trace/log pro modelo (padrão ligado;
                         ou JAZZ_PARSE_OUTPUT=0|1)
  --grep-trunc M         corte de saída longa de rg/grep: grouped (padrão, até 5 por arquivo) ou headtail
                         (ou JAZZ_GREP_TRUNC)
  --tool-budget N        bytes de um resultado do bash que o modelo vê (padrão auto: 8% da janela,
                         entre 6 e 30 KB; ex.: 12k; ou JAZZ_TOOL_BUDGET)
  --prefetch M           off (padrão) ou grep: no 1º turno, busca com rg os nomes citados no pedido
                         (ou JAZZ_PREFETCH)
  --parallel-tools M     chamadas só-leitura da mesma resposta rodam em paralelo: auto (padrão; com 1 CPU
                         só as de rede) · on · off · N (ou JAZZ_PARALLEL_TOOLS; config tools.parallel)
  --redact M             segredos em arquivos/saídas viram <secreto:tipo#n> antes de ir ao provedor:
                         on (padrão) · strict (também média confiança) · off (ou JAZZ_REDACT; config redact)
  --budget B             orçamento de passos/exploração: lean (padrão) · normal (dobro, sem parada) · off
  --quality Q            checagem de qualidade ao terminar: standard (padrão) · strict · off
  --guards on|off        guardas de conclusão honesta: testes protegidos, sem hardcode, remoção conferida, testes rodados no fim (padrão on)
  --recover M            travou (laço, --until igual 3×, 8 erros seguidos): recomeça com contexto limpo
                         on (padrão; desfaz se as falhas cresceram) · handoff · discard · off
  --subagents-parallel N quantos subagentes (task) rodam ao mesmo tempo (padrão auto: 3 com modelo remoto,
                         2 com servidor local; 1 = um por vez; config subagents.parallel; JAZZ_SUBAGENTS_PARALLEL)
  --debug                log detalhado em ~/.local/share/jazz-cli/logs (ou JAZZ_DEBUG=1)
  --banner               abre com o logo grande (padrão só em terminal com 100+ colunas)
  --scripts on|off       ferramenta script (scripts prontos que devolvem JSON; padrão: "scripts" da config, on)
  --ui-lint on|off       depois de write/edit em arquivo de UI (html, css, tsx, vue…), roda o ui_lint e anexa
                         os achados ao resultado (padrão on; config ui_lint; JAZZ_UI_LINT)
  --fast-path            (com -p) pedido claro ("rode os testes", "git status") roda o comando sem chamar o modelo
  --offline              modo offline: sem modelo remoto (atalhos, /script, !comando); também JAZZ_OFFLINE=1
  --low-power[=off]      modo econômico (menos quadros/animação; automático com 1 CPU); também JAZZ_LOW_POWER
  --ask-stdin            com -p: a ferramenta ask_user lê a resposta do stdin (sem ela, o modelo escolhe e explica)
  --allow-outside        deixa as ferramentas lerem/mexerem fora da pasta do projeto (padrão: só dentro dela,
                         mais pastas do sistema; config allow_outside_project; JAZZ_ALLOW_OUTSIDE=1)
  --block-ux on|off      bloqueio que orienta o modelo e para o turno após 3 tentativas bloqueadas no mesmo
                         alvo ou 6 no turno (padrão on; config block_ux; JAZZ_BLOCK_UX)
  --diff-guard on|off|strict  no fim de cada turno revisa os arquivos que o jazz mexeu (TLS desligado, segredo,
                         dependência suspeita, CI/CD…) e pergunta antes de aceitar o que for de risco
                         (padrão on; config diff_guard; JAZZ_DIFF_GUARD; veja jazz scan --rules)
  --cutoff-continue on|off  write/edit cortado no limite de saída: pede a continuação e junta as partes
                         (até 3; padrão on; config cutoff_continue; JAZZ_CUTOFF_CONTINUE)
  --lessons on|off       lições de .jazz/lessons.md e ~/.config/jazz-cli/lessons.md no início da sessão
                         (padrão on; config lessons; JAZZ_LESSONS; veja jazz improve)
  --sandbox M            isolamento do sistema pra tudo que o jazz executa (shell, jobs, hooks, MCP, scripts):
                         auto (padrão: sandbox-exec no macOS, bubblewrap no Linux; sem backend roda sem) ·
                         strict (recusa rodar sem backend; filhos só com rede loopback) · off
                         (config sandbox / sandbox_net; JAZZ_SANDBOX, JAZZ_SANDBOX_NET)

códigos de saída (-p): 0 ok · 1 erro · 2 uso inválido · 3 limite de passos · 4 laço detectado
                       5 ferramenta negada · 6 resposta cortada · 7 resposta não verificada
                       8 --until ainda falhando · 9 checagem de qualidade falhando
                       124 tempo esgotado (--timeout) · 130 interrompido
                       situação: 0 = concluída · 5 = negada · qualquer outro = incompleta (retome com -c / -r)
`)
}

// pfOpts are the flags shared by the interactive and headless paths.
type pfOpts struct {
	cont         bool
	resumeID     string
	verbose      bool
	format       string
	allowTools   []string
	systemAppend string
	timeout      time.Duration // headless deadline for the whole run; 0 = none
	banner       bool          // big ANSI-shadow logo instead of the welcome box
	loop         LoopOpts      // --until, --until-rounds, --review (agent_loop.go)
	scripts      string        // --scripts on|off ("" = config)
	uiLint       string        // --ui-lint on|off ("" = config) (uilint.go)
	fastPath     bool          // --fast-path: intent fast paths in -p (intents.go)
	offline      bool          // --offline (offline.go)
	modelPinned  bool          // -m given or a model chosen in the config (no interactive fast-first)
	askStdin     bool          // --ask-stdin: ask_user reads the answer from stdin in -p
	grace        time.Duration // --timeout-grace (obs.go); 0 = auto
	metricsOut   string        // --metrics-out (obs.go)
	resumeAt     string        // -r <id>@<entry>: resume at a node of the session tree
}

// pfScriptsOn decides whether the script tool is exposed: --scripts wins over
// JAZZ_SCRIPTS, which wins over config "scripts"; default on.
func pfScriptsOn(cfg *Config, flag string) bool {
	if strings.TrimSpace(flag) != "" {
		return !olOffWord(flag)
	}
	if e := strings.TrimSpace(os.Getenv("JAZZ_SCRIPTS")); e != "" {
		return !olOffWord(e)
	}
	return !olOffWord(cfg.Scripts)
}

// pfDefaultTimeout is the -p deadline when --timeout is not given.
const pfDefaultTimeout = 10 * time.Minute

// pfHardExitGrace: after the deadline cancels the run, how long cleanup may take
// before the process exits anyway (a -p run must never hang forever).
var pfHardExitGrace = 15 * time.Second

func main() {
	defer pfCrashGuard()
	pfTuneRuntime() // 1 CPU: less GC work (ui_width.go)
	pfCleanOldExe()
	fs := flag.NewFlagSet("jazz", flag.ContinueOnError)
	fs.Usage = usage
	pr := fs.Bool("p", false, "")
	fs.BoolVar(pr, "print", false, "")
	model := fs.String("m", "", "")
	fs.StringVar(model, "model", "", "")
	cont := fs.Bool("c", false, "")
	fs.BoolVar(cont, "continue", false, "")
	resume := fs.String("r", "", "")
	fs.StringVar(resume, "resume", "", "")
	dir := fs.String("C", "", "")
	yolo := fs.Bool("yolo", false, "")
	ackNoSandbox := fs.Bool("ack-no-sandbox", false, "")
	ask := fs.Bool("ask", false, "")
	smart := fs.Bool("smart", false, "")
	turbo := fs.Bool("turbo", false, "")
	fullAuto := fs.Bool("full-auto", false, "")
	verbose := fs.Bool("v", false, "")
	showVer := fs.Bool("version", false, "")
	format := fs.String("output-format", "text", "")
	maxSteps := fs.Int("max-steps", 0, "")
	timeout := fs.Duration("timeout", pfDefaultTimeout, "")
	allowTools := fs.String("allow-tools", "", "")
	fs.StringVar(allowTools, "allowedTools", "", "")
	sysAppend := fs.String("system-append", "", "")
	fs.StringVar(sysAppend, "append-system-prompt", "", "")
	debugFlag := fs.Bool("debug", false, "")
	bannerFlag := fs.Bool("banner", false, "")
	untilFlag := fs.String("until", "", "")
	fs.Var(&qFollowFlag, "follow-up", "") // queue.go
	untilRounds := fs.Int("until-rounds", 0, "")
	reviewFlag := fs.Bool("review", false, "")
	opFl := opRegisterFlags(fs) // --parse-output, --grep-trunc, --tool-budget, --prefetch (outshape.go)
	ptFl := ptRegisterFlags(fs) // --parallel-tools (partools.go)
	rdFl := rdRegisterFlags(fs) // --redact (redact.go)
	budgetFlag := fs.String("budget", "", "")
	qualityFlag := fs.String("quality", "", "")
	guardsFlag := fs.String("guards", "", "")
	scriptsFlag := fs.String("scripts", "", "")
	uiLintFlag := fs.String("ui-lint", "", "")
	fastPathFlag := fs.Bool("fast-path", false, "")
	offlineFlag := fs.Bool("offline", false, "")
	var lowPowerFlag uiLowFlag
	fs.Var(&lowPowerFlag, "low-power", "")
	askStdinFlag := fs.Bool("ask-stdin", false, "")
	recoverFlag := fs.String("recover", "", "")
	fs.IntVar(&saFlagParallel, "subagents-parallel", 0, "")
	allowOutside := fs.Bool("allow-outside", false, "")
	fs.BoolVar(&h2AllowTampered, "allow-tampered", false, "") // resume a session altered outside jazz (hardening2.go)
	blockUXFlag := fs.String("block-ux", "", "")
	diffGuardFlag := fs.String("diff-guard", "", "")
	cutoffFlag := fs.String("cutoff-continue", "", "")
	stdinFlag := fs.Bool("stdin", false, "")
	sandboxFlag := fs.String("sandbox", "", "")
	resumeLastIn := fs.String("resume-last-in", "", "")
	graceFlag := fs.Duration("timeout-grace", 0, "")
	metricsOut := fs.String("metrics-out", "", "")
	otlpEndpoint := fs.String("otlp-endpoint", "", "")
	lessonsFlag := lsFlag(fs)                               // --lessons on|off (lessons.go)
	ojFl := ojRegisterFlags(fs)                             // --omni-judge, --verifier, --hedge, --panel, --fusion (judge.go)
	wtArgs, wtOn, wtName, wtErr := wtParseArgs(os.Args[1:]) // --worktree [name] (worktree.go)
	if wtErr != nil {
		pfUsageErr(wtErr.Error())
	}
	args := reorderFlags(pfResumeArgs(wtArgs)) // bare -r opens the session list (sessions_ui.go)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		os.Exit(2)
	}
	if err := opFl.apply(); err != nil {
		pfUsageErr(err.Error())
	}
	if err := rdFl.apply(); err != nil {
		pfUsageErr(err.Error())
	}
	if err := ptFl.apply(); err != nil {
		pfUsageErr(err.Error())
	}
	if err := ojFl.apply(); err != nil {
		pfUsageErr(err.Error())
	}
	if *showVer {
		fmt.Println("jazz-cli", version)
		return
	}
	switch *format {
	case "text", "json", "stream-json":
	default:
		pfUsageErr("--output-format deve ser text, json ou stream-json")
	}
	if *maxSteps < 0 {
		pfUsageErr("--max-steps deve ser positivo")
	}
	if *recoverFlag != "" && !rcValidMode(*recoverFlag) {
		pfUsageErr("--recover deve ser on, off, handoff ou discard")
	}
	if *untilRounds < 0 {
		pfUsageErr("--until-rounds deve ser positivo")
	}
	if saFlagParallel < 0 {
		pfUsageErr("--subagents-parallel deve ser positivo")
	}
	switch *budgetFlag {
	case "", bgLean, bgNormal, bgOff:
	default:
		pfUsageErr("--budget deve ser lean, normal ou off")
	}
	switch *qualityFlag {
	case "", qgOff, qgStandard, qgStrict:
	default:
		pfUsageErr("--quality deve ser off, standard ou strict")
	}
	switch *guardsFlag {
	case "", "on", "off":
	default:
		pfUsageErr("--guards deve ser on ou off")
	}
	switch *blockUXFlag {
	case "", "on", "off":
	default:
		pfUsageErr("--block-ux deve ser on ou off")
	}
	switch *diffGuardFlag {
	case "", "on", "off", "strict":
	default:
		pfUsageErr("--diff-guard deve ser on, off ou strict")
	}
	switch *cutoffFlag {
	case "", "on", "off":
	default:
		pfUsageErr("--cutoff-continue deve ser on ou off")
	}
	if *timeout < 0 {
		pfUsageErr("--timeout deve ser positivo (ex.: 90s, 10m; 0 = sem prazo)")
	}
	if *debugFlag || os.Getenv("JAZZ_DEBUG") == "1" {
		if _, err := pfDebugStart(); err != nil {
			fmt.Fprintln(os.Stderr, "jazz: não consegui abrir o log de debug:", err)
		}
	}
	cfg, err := LoadConfig()
	if err != nil {
		fatal(err.Error())
	}
	pfNoteConfig(cfg)
	if err := otlpConfigure(cfg, *otlpEndpoint); err != nil {
		pfUsageErr(err.Error())
	}
	if err := lsConfigure(cfg, *lessonsFlag); err != nil {
		pfUsageErr(err.Error())
	}
	if *maxSteps > 0 {
		cfg.MaxSteps = *maxSteps
	}
	root, _ := os.Getwd()
	if *dir != "" {
		root, _ = filepath.Abs(*dir)
	}
	// --resume-last-in DIR: the eval runner's second attempt resumes the
	// newest session of DIR instead of starting over (obs.go)
	lastID := ""
	if *resumeLastIn != "" {
		d, _ := filepath.Abs(*resumeLastIn)
		if *dir == "" {
			root = d
		}
		if list := ListSessions(d); len(list) > 0 {
			lastID = list[0].ID
		} else {
			fmt.Fprintln(os.Stderr, "jazz: nenhuma sessão anterior em", d, "— começando uma nova")
		}
	}
	rest := fs.Args()
	if wtOn && (len(rest) == 0 || !pfSubcommands[rest[0]]) {
		r, err := wtStart(root, wtName, os.Stderr)
		if err != nil {
			fatal(err.Error())
		}
		root = r
	}
	// OS sandbox for every child process (sandbox.go); strict without a
	// backend refuses to run anything except the diagnostic subcommands
	if _, err := sbSetup(cfg, root, *sandboxFlag); err != nil {
		if errors.Is(err, errSandboxMissing) && len(rest) > 0 && (rest[0] == "doctor" || rest[0] == "version" || rest[0] == "help") {
			fmt.Fprintln(os.Stderr, "jazz:", err)
		} else if errors.Is(err, errSandboxMissing) {
			fatal(err.Error() + "; use --sandbox auto (ou off) para rodar sem isolamento")
		} else {
			pfUsageErr(err.Error())
		}
	}
	if len(rest) > 0 {
		switch rest[0] {
		case "login":
			cmdLogin(cfg, rest[1:])
			return
		case "logout":
			if len(rest) < 2 {
				pfUsageErr("uso: jazz logout <provedor>")
			}
			if err := SaveCred(rest[1], nil); err != nil {
				fatal(err.Error())
			}
			fmt.Println("credencial de", rest[1], "removida")
			return
		case "key":
			if len(rest) < 2 {
				pfUsageErr("uso: jazz key <provedor>   (a chave é lida escondida, ou do stdin)")
			}
			cmdKey(cfg, rest[1], nil)
			return
		case "models":
			p := ""
			if len(rest) > 1 {
				p = rest[1]
			}
			if p == "--scores" || p == "-scores" {
				ojPrintScores(cfg, os.Stdout)
				return
			}
			cmdModels(cfg, p, os.Stdout, DetectTheme())
			return
		case "provider", "providers":
			cmdProvider(cfg, rest[1:])
			return
		case "omni":
			if len(rest) > 2 && rest[1] == "setup" && (rest[2] == "--list" || rest[2] == "list") {
				omniSetupList(cfg, os.Stdout)
				return
			}
			if len(rest) > 1 && rest[1] == "setup" {
				cmdOmniSetup(cfg, os.Stdout)
				return
			}
			m := cfg.Model
			if m == "" {
				m = cfg.PickDefaultModel()
			}
			out, err := pfOmniCommand(cfg, m, rest[1:])
			if err != nil {
				pfUsageErr(err.Error())
			}
			fmt.Print(out)
			return
		case "export":
			cmdExport(cfg, root, rest[1:])
			return
		case "trust":
			cmdTrust(root, rest[1:])
			return
		case "deps":
			os.Exit(cmdDeps(root, rest[1:]))
		case "sanitize":
			os.Exit(cmdSanitize(rest[1:], os.Stdout))
		case "update", "upgrade":
			cmdUpdate(cfg, rest[1:])
			return
		case "bug":
			cmdBug(cfg, root)
			return
		case "script":
			os.Exit(cmdScript(root, rest[1:]))
		case "mcp":
			os.Exit(cmdMCP(rest[1:], os.Stdout)) // presets (mcp_presets.go)
		case "worktree", "worktrees":
			os.Exit(cmdWorktree(root, rest[1:], os.Stdin, os.Stdout))
		case "scan":
			os.Exit(cmdScan(root, rest[1:], os.Stdout)) // diff-review rule engine (diffguard_cmd.go)
		case "doctor":
			cmdDoctor(cfg, root, rest[1:], *verbose || *debugFlag || os.Getenv("JAZZ_DEBUG") == "1")
			return
		case "version":
			fmt.Println("jazz-cli", version)
			return
		case "help":
			usage()
			return
		case "trace":
			os.Exit(cmdTrace(root, rest[1:], os.Stdout))
		case "sessions":
			os.Exit(cmdSessions(root, rest[1:], os.Stdout))
		case "audit":
			os.Exit(cmdAudit(rest[1:], os.Stdout))
		case "changes", "blame":
			os.Exit(cmdChanges(root, rest[1:], os.Stdout))
		case "improve":
			os.Exit(cmdImprove(cfg, root, pfImproveModel(cfg, *model), rest[1:], os.Stdout))
		}
	}
	if *allowOutside {
		cfg.AllowOutsideRun = true
	}
	if *blockUXFlag != "" {
		cfg.BlockUXRun = *blockUXFlag
	}
	if *diffGuardFlag != "" {
		cfg.DiffGuardRun = *diffGuardFlag
	}
	if *cutoffFlag != "" {
		cfg.CutoffContinueRun = *cutoffFlag
	}
	if *yolo {
		cfg.ModeOverride = "yolo"
	} else if *ask {
		cfg.ModeOverride = "ask"
	} else if *smart {
		cfg.ModeOverride = "smart"
	} else if *turbo || *fullAuto {
		cfg.ModeOverride = "turbo"
	}
	ref := *model
	if ref == "" {
		ref = cfg.Model
	}
	if ref == "" {
		ref = cfg.PickDefaultModel()
	}
	if r, err := cfg.ResolveModel(ref); err == nil {
		ref = r
	} else {
		pfUsageErr(err.Error())
	}
	safeGo(cleanSpill)

	o := pfOpts{cont: *cont, resumeID: *resume, verbose: *verbose, format: *format, systemAppend: *sysAppend, timeout: *timeout, banner: *bannerFlag}
	o.loop = LoopOpts{Until: *untilFlag, UntilRounds: *untilRounds, Review: *reviewFlag, Budget: *budgetFlag, Quality: *qualityFlag, Guards: *guardsFlag, Recover: *recoverFlag}
	o.scripts, o.fastPath, o.offline = *scriptsFlag, *fastPathFlag, *offlineFlag
	o.modelPinned = *model != "" || (cfg.Model != "" && cfg.Model != "kilo/"+kiloDefaultModel)
	o.askStdin = *askStdinFlag
	lowPowerFlag.apply()
	o.uiLint = *uiLintFlag
	o.grace, o.metricsOut = *graceFlag, *metricsOut
	if id, at, ok := strings.Cut(o.resumeID, "@"); ok {
		o.resumeID, o.resumeAt = id, at
	}
	if lastID != "" {
		o.resumeID = lastID
	}
	if strings.TrimSpace(*allowTools) != "" {
		o.allowTools = pfSplitFlag(*allowTools)
	}
	forceStdin := *stdinFlag
	if n := len(rest); n > 0 && rest[n-1] == "-" { // `jazz -p "x" -` / `jazz -p -`: read stdin to EOF
		rest, forceStdin = rest[:n-1], true
	}
	prompt := strings.Join(rest, " ")
	stdinTTY := isTTY(os.Stdin)
	if pfHeadlessYoloNeedsAck(cfg.EffMode(), turboSandboxOK(), *pr || !stdinTTY || forceStdin) && !*ackNoSandbox {
		pfUsageErr("YOLO headless sem sandbox do sistema não é isolado; passe --ack-no-sandbox para confirmar explicitamente")
	}
	if *pr || !stdinTTY || forceStdin {
		if !stdinTTY || forceStdin {
			wait := siWaitArg
			if prompt == "" {
				wait = siWaitNoArg
			}
			if s := strings.TrimSpace(readStdinPrompt(os.Stdin, forceStdin, wait, siIdle)); s != "" {
				if prompt != "" {
					prompt += "\n\n" + s
				} else {
					prompt = s
				}
			}
		}
		if prompt == "" && !o.cont && o.resumeID == "" {
			pfUsageErr("nada pra fazer: passe um prompt (jazz -p \"...\")")
		}
		if o.resumeID == pfPickResume {
			pfUsageErr("jazz -p -r precisa do id da sessão (jazz sessions lista; -c continua a última)")
		}
		code := runHeadless(cfg, root, ref, prompt, o)
		wtExitHint(os.Stderr)
		os.Exit(code)
	}
	runInteractive(cfg, root, ref, prompt, o)
	wtExitHint(os.Stdout)
}

// pfSubcommands are the first words that take their own flags.
var pfSubcommands = map[string]bool{"login": true, "logout": true, "key": true, "models": true, "provider": true, "providers": true,
	"omni": true, "export": true, "trust": true, "update": true, "upgrade": true, "bug": true, "doctor": true, "version": true, "help": true,
	"script": true, "deps": true, "sanitize": true, "trace": true, "sessions": true, "audit": true, "changes": true, "blame": true, "improve": true, "worktree": true, "worktrees": true, "scan": true, "mcp": true}

// pfGlobalAfterSub are the global flags still honoured after a subcommand word.
var pfGlobalAfterSub = map[string]bool{"--yolo": true, "-yolo": true, "--ask": true, "-ask": true, "--smart": true, "-smart": true, "--turbo": true, "-turbo": true, "--full-auto": true, "-full-auto": true, "-v": true, "--debug": true, "-debug": true, "-C": true, "--sandbox": true, "-sandbox": true}

// reorderFlags lets flags appear after positional words: jazz "fix it" -m x.
// After a subcommand (jazz trust --yes, jazz provider add x --model m) only a
// few global flags are moved; the rest belong to the subcommand.
func reorderFlags(a []string) []string {
	valued := map[string]bool{"-m": true, "--model": true, "-C": true, "-r": true, "--resume": true,
		"--output-format": true, "-output-format": true, "--max-steps": true, "-max-steps": true, "--timeout": true, "-timeout": true, "--timeout-grace": true, "--metrics-out": true, "--otlp-endpoint": true, "--resume-last-in": true,
		"--allow-tools": true, "-allow-tools": true, "--allowedTools": true, "--system-append": true, "-system-append": true,
		"--append-system-prompt": true, "--until": true, "-until": true, "--until-rounds": true, "-until-rounds": true,
		"--budget": true, "-budget": true, "--quality": true, "-quality": true, "--guards": true, "-guards": true,
		"--recover": true, "-recover": true, "--subagents-parallel": true, "-subagents-parallel": true, "--scripts": true, "-scripts": true, "--block-ux": true, "-block-ux": true, "--diff-guard": true, "-diff-guard": true, "--cutoff-continue": true, "-cutoff-continue": true,
		"--ui-lint": true, "-ui-lint": true, "--sandbox": true, "-sandbox": true}
	var flags, pos []string
	sub := false
	for k := range opValuedFlags {
		valued[k] = true
	}
	for k := range lsValuedFlags {
		valued[k] = true
	}
	for i := 0; i < len(a); i++ {
		s := a[i]
		if s == "--" {
			pos = append(pos, a[i+1:]...)
			break
		}
		if strings.HasPrefix(s, "-") && len(s) > 1 {
			if sub && !pfGlobalAfterSub[s] {
				pos = append(pos, s)
				continue
			}
			flags = append(flags, s)
			if valued[s] && i+1 < len(a) {
				flags = append(flags, a[i+1])
				i++
			}
			continue
		}
		if len(pos) == 0 && pfSubcommands[s] {
			sub = true
		}
		pos = append(pos, s)
	}
	return append(flags, pos...)
}

func fatal(msg string) {
	fmt.Fprintln(os.Stderr, "jazz:", msg)
	os.Exit(1)
}

// pfUsageErr reports a usage error (exit 2).
func pfUsageErr(msg string) {
	fmt.Fprintln(os.Stderr, "jazz:", msg)
	os.Exit(2)
}

// pfFindSession picks the session to continue (id == "": newest) in root.
func pfFindSession(root, id string) (SessionInfo, error) {
	list := ListSessions(root)
	if len(list) == 0 {
		return SessionInfo{}, errors.New("nenhuma sessão anterior nesta pasta")
	}
	if id == "" {
		return list[0], nil
	}
	for _, s := range list {
		if s.ID == id || strings.HasPrefix(s.ID, id) {
			return s, nil
		}
	}
	return SessionInfo{}, errors.New("sessão " + id + " não encontrada")
}

// pfSetupAgent wires permission rules and hooks; it must run before the first Run.
func pfSetupAgent(ag *Agent, cfg *Config, root string, o pfOpts) {
	ag.Perms = LoadPerms(cfg, root, o.allowTools)
	ag.Hooks = LoadHooks(cfg, root, pfProjectPolicyTrusted(root))
	ag.SetLoopOpts(o.loop)
	ag.AskStdin = o.askStdin
	dcConfigure(o.offline) // docs tool: --offline serves the cache only (docs.go)
}

// pfStartSession builds the system prompt (once), applies --system-append and
// runs the SessionStart hooks (their output becomes a note for the first turn,
// never part of System, so the prompt cache holds).
func pfStartSession(ctx context.Context, ag *Agent, o pfOpts, source string) {
	ag.EnsureSystem()
	if o.systemAppend != "" && !strings.HasSuffix(ag.System, o.systemAppend) {
		ag.System += "\n\n" + o.systemAppend
	}
	if ag.Hooks.Has(HookSessionStart) {
		r := ag.Hooks.Run(ctx, HookSessionStart, "", map[string]any{"session_id": ag.SessionID, "source": source})
		if strings.TrimSpace(r.Context) != "" {
			ag.Note("<system-reminder>SessionStart hook output:\n" + strings.TrimSpace(r.Context) + "</system-reminder>")
		}
	}
}

func runHeadless(cfg *Config, root, ref, prompt string, o pfOpts) int {
	var ui HeadlessSink
	switch o.format {
	case "json":
		ui = NewJSONUI(os.Stdout, false)
	case "stream-json":
		ui = NewJSONUI(os.Stdout, true)
	default:
		ui = &HeadlessUI{Verbose: o.verbose}
	}
	pfTrustGateWith(root, true, false, nil, os.Stderr)
	tools := NewToolbox(root)
	tools.Web = cfg.Web
	tools.MaxJobs = cfg.MaxJobs
	tools.Scripts = pfScriptsOn(cfg, o.scripts)
	tools.UILint = pfUILintOn(cfg, o.uiLint)
	// shadow-git checkpoints: the work of an incomplete run is kept and traced
	// (checkpoint_trace.go); JAZZ_CHECKPOINTS=0 turns them off
	tools.Checkpoints = os.Getenv("JAZZ_CHECKPOINTS") != "0"
	mcp := StartMCP(root)
	pfOnCrash(mcp.pfKillAll)
	pfOnCrash(tools.Shell.killGroup)
	ag := NewAgent(cfg, ui, tools, mcp, ref)
	pfSetupAgent(ag, cfg, root, o)
	ag.Headless, ag.PrivacyCheck = true, true
	pfOnCrash(func() { obCrash(ag, tools) }) // checkpoint + handoff + end record (obs.go)
	auConfigStart(cfg, root, ref, pfSandboxStatus(), pfAuditFields(ag, o, true))
	// untrusted folder (F1): bash that is not read-only needs --allow-tools rules or --yolo
	ag.UntrustedBash = !pfFolderTrusted(root) && cfg.EffMode() != "yolo"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sig)
	safeGo(func() {
		if _, ok := <-sig; ok {
			cancel()
			tools.Shell.killGroup() // the running command dies now, not at its timeout (bug 40)
		}
	})
	timedOut, stopDeadline := pfDeadline(o.timeout, func() {
		cancel()
		tools.Shell.killGroup()
	})
	defer stopDeadline()
	ojTimedOut = timedOut // the judge scores a --timeout as a timeout, not an interruption (judge.go)
	if o.cont || o.resumeID != "" {
		si, err := pfFindSession(root, o.resumeID)
		switch {
		case err != nil && o.resumeID != "":
			fmt.Fprintln(os.Stderr, "jazz:", err)
			mcp.Close()
			return 2
		case err != nil:
			ui.Warn(err.Error() + " — começando uma nova")
		default:
			if err := ag.LoadSessionAt(si, o.resumeAt); err != nil {
				fmt.Fprintln(os.Stderr, "jazz: não consegui abrir a sessão:", err)
				mcp.Close()
				return 1
			}
			if h2TamperedAbort(ag.sess) { // altered session: untrusted by default, headless refuses (hardening2.go)
				fmt.Fprintln(os.Stderr, "jazz: "+h2SessionNotice(ag.sess.Tampered)+"; use --allow-tampered para retomar mesmo assim")
				mcp.Close()
				return 5
			}
			// an incomplete run left a handoff note: it leads the next message
			// (a user message, never the system prompt: the cache holds)
			if note := obResumeNote(si.Path); note != "" {
				if strings.TrimSpace(prompt) == "" {
					prompt = "Continue the task from where the previous run stopped."
				}
				prompt = note + "\n\n" + prompt
				ui.Notice("retomando a sessão " + si.ID + " de onde parou")
			}
		}
	}
	if strings.TrimSpace(prompt) == "" {
		prompt = "Continue the task where you left off. If it is already done and verified, say so briefly."
	}
	source := "startup"
	if len(ag.Msgs) > 0 {
		source = "resume"
	}
	pfStartSession(ctx, ag, o, source)
	for _, w := range []string{AuthWarning(), ChatGPTWarning(), cfg.ModelWarning(ref), turboStartNote(cfg.EffMode())} {
		if w != "" {
			fmt.Fprintln(os.Stderr, "jazz:", w)
		}
	}
	var names []string
	for _, s := range tools.Specs() {
		names = append(names, s.Name)
	}
	for _, s := range mcp.Specs() {
		names = append(names, s.Name)
	}
	t0 := time.Now()
	// deadline grace: past timeout-grace no new model request starts; the
	// tool in flight finishes, then checkpoint + handoff (obs.go)
	if grace := obGrace(o.timeout, o.grace); grace > 0 {
		soft := time.AfterFunc(o.timeout-grace, func() {
			ag.SoftStop(fmt.Sprintf("faltam %s para o --timeout %s", grace, o.timeout))
			ui.Warn(fmt.Sprintf("prazo quase no fim (faltam %s): termino a ferramenta em curso e salvo o trabalho", grace.Round(time.Second)))
		})
		defer soft.Stop()
	}
	ui.Begin(RunInfo{SessionID: ag.SessionID, Model: ag.Model, Cwd: root, Tools: names})
	// fast path / offline mode (offline.go): fp != nil = answered without the model
	fp, mprompt := olHeadless(ctx, ag, tools, ui, cfg, prompt, o)
	var stop StopReason
	var err error
	if fp == nil {
		stop, err = ag.qRunHeadless(ctx, mprompt) // + --follow-up turns, JAZZ_STEER_FILE (queue.go)
		if err != nil && ctx.Err() == nil {
			fp = olHeadlessFallback(ctx, ag, tools, ui, err, prompt)
		}
	}
	FlushRateLimits()
	if stop == "" {
		stop = StopDone
		if err != nil {
			stop = StopError
		}
	}
	if ctx.Err() != nil {
		stop = StopInterrupted
	}
	res := RunResult{Status: stop, Usage: ag.Usage, Cost: ag.Usage.Cost, Duration: time.Since(t0), SessionID: ag.SessionID, Model: ag.TurnModel(), Recover: ag.LastRecovery()}
	if err != nil && stop != StopInterrupted {
		res.Status, res.Err = StopError, friendlyErr(err)
	}
	if fp != nil && ctx.Err() == nil {
		fp.SessionID, fp.Duration = ag.SessionID, time.Since(t0)
		res = *fp
	}
	if ag.BlockReport != "" && fp == nil {
		// the spiral breaker stopped the turn: the report is the answer, not
		// whatever the model said before its last blocked attempt (blockux.go)
		res.Text = ag.BlockReport
	}
	if timedOut() {
		res.Status, res.Err = StopTimeout, pfTimeoutMsg(o.timeout)
	}
	if ag.obSoftStopped() && res.Status == StopTimeout && res.Err == "" {
		res.Err = pfTimeoutMsg(o.timeout) + " (parado antes, com folga, e salvo)"
	}
	// no work lost: checkpoint, handoff note, end record, fsync (obs.go)
	end := obFinishRun(ag, tools, res, res.Status.ExitCode())
	if o.metricsOut != "" && ag.sess != nil {
		if err := obWriteMetrics(o.metricsOut, ag.sess.Path); err != nil {
			fmt.Fprintln(os.Stderr, "jazz: --metrics-out:", err)
		}
	}
	ag.Close()
	imAutoAfterRun(cfg, root, ag, timedOut()) // improve.auto: learn from this run (improve.go)
	if j, ok := ui.(*JSONUI); ok {
		j.Extra = map[string]any{"outcome": end.Outcome}
		if end.Outcome != obCompleted {
			j.Extra["checkpoint"], j.Extra["handoff"] = end.Checkpoint, end.Handoff
			j.Extra["resume"] = "jazz -p -r " + ag.SessionID
		}
	}
	ui.Finish(res)
	if res.Err != "" && o.format == "text" {
		fmt.Fprintln(os.Stderr, "jazz:", res.Err)
	}
	if end.Outcome != obCompleted && ag.SessionID != "" {
		fmt.Fprintf(os.Stderr, "jazz: execução %s (%s) — trabalho salvo: checkpoint %s · nota %s\njazz: retome com: jazz -p -r %s   (ou jazz -p --resume-last-in %s)\n",
			obOutcomePT(end.Outcome), res.Status, orDash(obShort(end.Checkpoint)), orDash(end.Handoff), ag.SessionID, root)
	}
	if n := tools.KillJobs(); n > 0 && o.verbose {
		fmt.Fprintf(os.Stderr, "· %d job(s) em segundo plano encerrado(s)\n", n)
	}
	tools.Shell.Close()
	mcp.Close()
	return res.Status.ExitCode()
}

// pfDeadline arms the headless --timeout: when d passes, onTimeout runs (cancel
// the run, kill the shell) and, if the process is still alive after
// pfHardExitGrace, it exits with the timeout code. d <= 0 = no deadline.
func pfDeadline(d time.Duration, onTimeout func()) (timedOut func() bool, stop func()) {
	if d <= 0 {
		return func() bool { return false }, func() {}
	}
	var hit atomic.Bool
	var hard *time.Timer
	var mu sync.Mutex
	t := time.AfterFunc(d, func() {
		hit.Store(true)
		onTimeout()
		mu.Lock()
		hard = time.AfterFunc(pfHardExitGrace, func() {
			fmt.Fprintln(os.Stderr, "jazz:", pfTimeoutMsg(d), "(encerrado à força)")
			os.Exit(StopTimeout.ExitCode())
		})
		mu.Unlock()
	})
	return hit.Load, func() {
		t.Stop()
		mu.Lock()
		if hard != nil {
			hard.Stop()
		}
		mu.Unlock()
	}
}

func pfTimeoutMsg(d time.Duration) string {
	return fmt.Sprintf("tempo esgotado: a execução passou de --timeout %s", d)
}

func friendlyErr(err error) string {
	var re *RefusalError
	if errors.As(err, &re) {
		return re.Error() + " — reformule o pedido ou troque de modelo (/model)"
	}
	var ae *APIError
	if errors.As(err, &ae) {
		switch {
		case ae.Status == 401 || ae.Status == 403:
			return "credencial recusada pelo provedor (" + firstSentence(ae.Body) + ") — use /key ou /login"
		case ae.Status == 402 || pvClassify(ae) == "credit":
			return "sem crédito nesse provedor — troque de modelo (/model) ou adicione crédito"
		case ae.Status == 429:
			msg := "limite de requisições atingido (" + firstSentence(pvErrMessage(ae.Body)) + ")"
			if strings.Contains(strings.ToLower(ae.Body), "openrouter") {
				msg += ". Na OpenRouter grátis o limite é baixo"
			}
			return msg + "; espere ou troque de modelo (/model)"
		case ae == errNoNet:
			if p := pvProxyFor(); p != "" {
				return "o proxy " + p + " (HTTPS_PROXY) não responde"
			}
			return "nenhuma conexão: nem o provedor nem 1.1.1.1 responderam"
		case ae.Status == 0:
			return "falha de rede: " + firstSentence(ae.Body)
		case ae.Status >= 400 && ae.Status < 500:
			return fmt.Sprintf("o provedor recusou o pedido (HTTP %d: %s) — troque de modelo (/model); detalhes: jazz doctor --provider <nome>", ae.Status, firstSentence(pvErrMessage(ae.Body)))
		}
		return ae.Error()
	}
	if errors.Is(err, errStreamIdle) {
		return err.Error() + " — tente de novo ou troque de modelo"
	}
	return err.Error()
}

// ---------- interactive ----------

type App struct {
	cfg   *Config
	ui    *TermUI
	ed    *Editor
	agent *Agent
	tools *Toolbox
	mcp   *MCPManager
	root  string
	opts  pfOpts
	quit  bool

	started bool // system prompt built and SessionStart hooks ran

	turnMu     sync.Mutex
	cancelTurn func() // cancels the running turn (signal handler, bug 40)
	keepJobs   bool   // /exit chose to leave background jobs running

	warnedModel map[string]bool // ModelWarning already shown per ref

	ol olState // offline mode (offline.go)

	menu       []olAction   // pending numbered menu (offers.go): the next reply may pick from it
	olWatching atomic.Bool  // the offline background probe runs
	olGen      atomic.Int64 // bumped on every offline enter/leave (stops stale probes)
	olBack     atomic.Bool  // the background probe found a provider
	olNext     atomic.Int64 // when the background probe runs next (unix ns)

	untrusted    bool // the folder is not trusted (F1, /trust)
	untrustedAsk bool // ModeOverride "ask" was set because of that
}

func runInteractive(cfg *Config, root, ref, initial string, o pfOpts) {
	if cfg.Mode == "" {
		cfg.DefaultMode = pfDefaultMode() // turbo when an OS sandbox isolates (confirm.go)
	}
	fd := int(os.Stdin.Fd())
	if runtime.GOOS == "windows" && !termVT() {
		fatal("este console não aceita sequências VT/ANSI, que a interface do jazz precisa. Use o Windows Terminal (ou o console do Windows 10 1809+), ou o modo sem interface: jazz -p \"…\"")
	}
	if cfg.EffMode() == "yolo" && !turboSandboxOK() && !pfConfirmYoloWithoutSandbox() {
		cfg.ModeOverride = "auto" // keep the persisted choice intact, but start safely this session
		fmt.Fprintln(os.Stderr, "jazz: YOLO cancelado; iniciando em auto (sem sandbox do sistema)")
	}
	restore, err := enterCbreak(fd)
	if err != nil {
		fatal("terminal não suportado: " + err.Error())
	}
	var restoreOnce sync.Once
	restoreTerm := func() { restoreOnce.Do(restore) }
	pfOnCrash(restoreTerm)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGHUP)
	keys := NewKeyReader()
	ui := NewTermUI(cfg, keys)
	// the trust question (a menu) runs before anything project-level starts
	pfTrustGateUI(root, cfg, ui)
	untrusted := !pfFolderTrusted(root)
	untrustedAsk := false
	if untrusted {
		if m := pfUntrustedMode(cfg); m != "" {
			cfg.ModeOverride, untrustedAsk = m, true // F1: ask until the folder is trusted
		}
	}
	tools := NewToolbox(root)
	tools.Web = cfg.Web
	tools.Checkpoints = true // shadow-git: /undo and /rewind also cover bash changes
	tools.OnShellOutput = ui.ShellOutput
	tools.Scripts = pfScriptsOn(cfg, o.scripts)
	tools.UILint = pfUILintOn(cfg, o.uiLint)
	mcp := StartMCP(root)
	pfOnCrash(mcp.pfKillAll)
	pfOnCrash(tools.Shell.killGroup)
	tools.MaxJobs = cfg.MaxJobs
	app := &App{cfg: cfg, ui: ui, tools: tools, mcp: mcp, root: root, opts: o, untrusted: untrusted, untrustedAsk: untrustedAsk}
	app.agent = NewAgent(cfg, ui, tools, mcp, ref)
	pfSetupAgent(app.agent, cfg, root, o)
	app.agent.PrivacyCheck = true
	app.agent.NetOffline = true                     // a person is waiting: no network = offline mode now (netfast.go)
	app.agent.FastFirst = !o.modelPinned            // interactive: the fastest acceptable free model first
	pfOnCrash(func() { obCrash(app.agent, tools) }) // checkpoint + handoff (obs.go)
	auConfigStart(cfg, root, ref, pfSandboxStatus(), pfAuditFields(app.agent, o, false))
	var cleanOnce sync.Once
	cleanup := func() {
		cleanOnce.Do(func() {
			ui.StopThinking()
			if !app.keepJobs {
				tools.KillJobs()
			}
			tools.Shell.Close()
			mcp.Close()
			app.agent.Close()
			FlushRateLimits()
			restoreTerm()
		})
	}
	safeGo(func() {
		<-sig
		// SSH dropped or kill: stop the turn and the running command right away
		// (lock-free kill of the shell's process group), then clean up (bug 40)
		app.turnMu.Lock()
		if app.cancelTurn != nil {
			app.cancelTurn()
		}
		app.turnMu.Unlock()
		tools.Shell.killGroup()
		mcp.pfKillAll()
		done := make(chan struct{})
		go func() { cleanup(); close(done) }()
		select {
		case <-done:
		case <-time.After(4 * time.Second):
			restoreTerm()
		}
		os.Exit(130)
	})
	defer cleanup()

	app.ed = NewEditor(ui)
	app.ed.complete = app.completeCmd
	app.ed.CycleMode = app.cycleMode
	app.ed.Root = root
	app.ed.Footer = app.footer
	app.ed.LiveFooter = func() bool { return app.ol.on && !app.ol.forced } // offline countdown
	app.ed.Commands = app.edCommands
	ui.print(app.welcome())
	modeNoteSeen(cfg)
	// banner, then the resumed conversation, then the notice (sessions_ui.go)
	switch {
	case o.resumeID == pfPickResume:
		app.resumePick()
	case o.cont || o.resumeID != "":
		app.resumeAt(o.resumeID, o.resumeAt)
	}
	if !cfg.HasCreds(mustProvider(app.agent.Model)) {
		ui.Warn("sem credencial pra " + app.agent.Model + ". Use /key <provedor> ou /login (ChatGPT). Veja /help")
	}
	if p := pfDebugPath(); p != "" {
		ui.Notice("debug: log em " + p)
	}
	app.pfShowProviderWarnings()
	if untrustedAsk {
		ui.Notice("pasta não confiável: modo ask até você confiar (/trust)")
	}
	if n := turboStartNote(cfg.EffMode()); n != "" {
		ui.Warn(n)
	}
	if cfg.Omni.Enabled || isOmniRef(app.agent.Model) {
		if !cfg.Omni.PrivacyAck && !cfg.PrivacyAck {
			ui.Warn(OmniPrivacyNotice)
			cfg.Omni.PrivacyAck, cfg.PrivacyAck = true, true
			_ = cfg.Save()
		}
		safeGo(func() { // free catalogs and OpenRouter's daily quota, in the background
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			OmniRefresh(ctx, cfg)
		})
	}
	if n := pfUpdateNotice(cfg); n != "" {
		ui.Notice(n)
	}
	ui.print(app.startLine())
	app.olStart() // JAZZ_OFFLINE / --offline / no candidate at all
	app.started = false
	if initial != "" {
		app.send(initial)
	}
	for !app.quit {
		ui.Jobs(tools.RunningJobs())
		ui.gapLocked()
		line, err := app.ed.ReadInput()
		if err != nil {
			if errors.Is(err, ErrEOF) && !app.confirmJobs(true) {
				continue
			}
			break
		}
		line = strings.TrimRight(line, " \t")
		if strings.TrimSpace(line) == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "/"):
			app.command(line)
		case strings.HasPrefix(line, "!"):
			app.shellEscape(strings.TrimSpace(line[1:]))
		default:
			app.send(line)
		}
	}
	ui.print(ui.T.fg(cGray, 90) + "  até mais " + ui.T.G().dot + " sessão " + app.agent.SessionID + " (jazz -c continua)" + ui.T.sgr("0") + "\n")
}

func mustProvider(ref string) string {
	p, _ := SplitModel(ref)
	return p
}

func (a *App) bannerInfo() []string {
	t := a.ui.T
	dim, b, r := t.fg(cGray, 90), t.sgr("1"), t.sgr("0")
	home, _ := os.UserHomeDir()
	dir := a.root
	if home != "" && strings.HasPrefix(dir, home) {
		dir = "~" + strings.TrimPrefix(dir, home)
	}
	return []string{
		t.fg(cGold, 33) + b + "jazz-cli" + r + dim + " v" + version + r,
		dim + "modelo " + r + a.agent.Model,
		dim + "pasta  " + r + dir,
		dim + "modo   " + r + a.cfg.EffMode() + dim + "  " + t.G().dot + "  /help" + pfHintSuffix(dim, t.G().dot, a.welcomeHint()) + r,
	}
}

// welcomeHint is what follows the mode on the start screen: the mode's
// one-line explanation the first time it is the start mode, then the turbo
// recommendation where it applies.
func (a *App) welcomeHint() string {
	note, hint := modeNote(a.cfg)
	if note != "" {
		return note
	}
	return hint
}

// welcome is the start screen: the compact box, or the big logo with
// --banner or on a terminal of 100+ columns.
func (a *App) welcome() string {
	w := termWidth()
	if a.opts.banner || w >= 100 {
		return Banner(a.ui.T, a.bannerInfo())
	}
	home, _ := os.UserHomeDir()
	dir := a.root
	if home != "" && strings.HasPrefix(dir, home) {
		dir = "~" + strings.TrimPrefix(dir, home)
	}
	note, hint := modeNote(a.cfg)
	return Welcome(a.ui.T, w, WelcomeInfo{Version: version, Model: a.agent.Model, Dir: dir, Mode: a.cfg.EffMode(), Hint: hint, Note: note})
}

// footer is the line under the input box.
func (a *App) footer() string {
	ctxTok := 0
	if len(a.agent.Msgs) > 0 {
		ctxTok = a.agent.TokenEstimate()
	}
	ready := 0
	for _, s := range a.mcp.Servers {
		if st, _, _ := s.State(); st == "ready" {
			ready++
		}
	}
	if off := a.olFooter(); off != "" {
		// offline: what matters goes first (the footer is clipped to one line)
		t := a.ui.T
		return "  " + t.fg(cGold, 33) + t.G().warn + " " + off + t.fg(cGray, 90) + " " + t.G().dot + " " + uiModeTag(t, a.cfg.EffMode()) + t.fg(cGray, 90) + " " + t.G().dot + " ? atalhos" + t.sgr("0")
	}
	return a.ui.Footer(a.agent.Model, ctxTok, a.agent.Window(), ready, len(a.mcp.Servers))
}

// edCommands feeds the "/" menu of the input box.
func (a *App) edCommands() []EdCommand {
	var out []EdCommand
	for _, c := range slashCommands {
		out = append(out, EdCommand{Name: c.name, Help: c.help})
	}
	for _, c := range customCommands(a.root) {
		out = append(out, EdCommand{Name: "/" + c.name, Help: c.desc})
	}
	return out
}

func (a *App) statusLine() string {
	ctxTok := 0
	if len(a.agent.Msgs) > 0 {
		ctxTok = a.agent.TokenEstimate()
	}
	ready := 0
	for _, s := range a.mcp.Servers {
		if st, _, _ := s.State(); st == "ready" {
			ready++
		}
	}
	return a.ui.StatusLine(a.agent.Model, ctxTok, a.agent.Window(), ready, len(a.mcp.Servers))
}

// ensureStarted builds the system prompt and runs SessionStart hooks once.
func (a *App) ensureStarted(ctx context.Context) {
	if a.started {
		return
	}
	a.started = true
	source := "startup"
	if len(a.agent.Msgs) > 0 {
		source = "resume"
	}
	pfStartSession(ctx, a.agent, a.opts, source)
}

func (a *App) send(text string) {
	var ok bool
	if text, ok = a.confirmPastedToolTranscript(text); !ok {
		return
	}
	defer a.qSend()() // message queue: next queued turn, or back to the editor (queue.go)
	ctx, cancel := context.WithCancel(context.Background())
	a.turnMu.Lock()
	a.cancelTurn = cancel
	a.turnMu.Unlock()
	defer func() {
		a.turnMu.Lock()
		a.cancelTurn = nil
		a.turnMu.Unlock()
		cancel()
	}()
	// fast paths and offline mode (offline.go): may answer without the model
	if handled, t := a.olBefore(ctx, text); handled {
		return
	} else {
		text = t
	}
	// honest feedback at once: the status line is up before anything slow
	// (session start, hooks, @mentions, the request itself)
	a.ui.Thinking("preparando")
	asked := text // the closure must not see the reassignment below (data race)
	safeGo(func() { a.olWaitOffers(asked) })
	defer a.ui.SetOffers(nil, nil)
	a.ensureStarted(ctx)
	text, imgs := ExpandMentions(a.tools, text)
	if len(imgs) > 0 {
		a.agent.Attach(imgs...)
	}
	stop := a.ui.WatchTurn(cancel, a.agent.Steer)
	reason, err := a.agent.Run(ctx, text)
	stop()
	FlushRateLimits()
	// text sent while the agent worked that the model never saw goes back to the editor (bug 39)
	if s := a.agent.TakeSteer(); s != "" {
		a.ed.Prefill(s)
	}
	a.pfShowProviderWarnings()
	a.ui.Jobs(a.tools.RunningJobs())
	if a.olAfter(err) { // no model reachable: offline mode banner instead of a bare error
		return
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		a.ui.Error(friendlyErr(err))
		return
	}
	if reason == StopDone && a.cfg.EffMode() == "plan" && ctx.Err() == nil {
		// G12: the plan is ready; let the user switch to execution in one key
		base := a.cfg.BaseMode()
		if base == "ask" || base == "plan" {
			base = "auto"
		}
		switch a.ui.Choose("Executar o plano?", []mnItem{
			{Label: "Sim, executar (modo " + base + ")", Key: 's', Keys: "y"},
			{Label: "Sim, perguntando antes de cada mudança (modo ask)", Key: 'a'},
			{Label: "Não, continuar planejando", Key: 'n'},
		}, 0) {
		case 0:
			a.setSessionMode(base)
		case 1:
			a.setSessionMode("ask")
		default:
			return
		}
		a.ui.Notice("modo " + a.cfg.EffMode() + ": executando o plano")
		a.send("Execute the plan above.")
	}
}

// pfShowProviderWarnings shows one-time provider warnings: a corrupt auth.json,
// a failed ChatGPT token save, and the Ollama num_ctx note for the model in use.
func (a *App) pfShowProviderWarnings() {
	for _, w := range []string{AuthWarning(), ChatGPTWarning()} {
		if w != "" {
			a.ui.Warn(w)
		}
	}
	ref := a.agent.TurnModel()
	if w := a.cfg.ModelWarning(ref); w != "" && !a.warnedModel[ref] {
		if a.warnedModel == nil {
			a.warnedModel = map[string]bool{}
		}
		a.warnedModel[ref] = true
		a.ui.Warn(w)
	}
}

// setSessionMode switches the effective mode for this session only.
func (a *App) setSessionMode(m string) {
	if m == a.cfg.BaseMode() {
		a.cfg.ModeOverride = ""
	} else {
		a.cfg.ModeOverride = m
	}
	a.ui.SetMode(a.cfg.EffMode())
}

// cycleMode is bound to Shift+Tab / Ctrl+T: turbo → auto → plan → ask →
// turbo (modeCycleNext; smart stays on /mode smart). The choice lasts for
// this session only (ModeOverride is never saved).
func (a *App) cycleMode() {
	a.setSessionMode(modeCycleNext(a.cfg.EffMode(), turboSandboxOK()))
}

func (a *App) shellEscape(cmd string) {
	if cmd == "" {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	stop := a.ui.WatchTurn(cancel, nil)
	a.ui.Thinking("rodando")
	r := a.tools.Shell.Run(ctx, cmd, 10*time.Minute, a.ui.ShellOutput)
	a.ui.StopThinking()
	stop()
	cancel()
	out := r.Output
	a.ui.print(sanitizeTerm(out))
	if out != "" && !strings.HasSuffix(out, "\n") {
		a.ui.print("\n")
	}
	a.ui.Notice(fmt.Sprintf("exit %d %s %s", r.ExitCode, a.ui.T.G().dot, strings.Trim(r.Note, "[]")))
	a.agent.Note(fmt.Sprintf("<system-reminder>The user ran `%s` in the shell themselves (exit %d). Output:\n%s</system-reminder>", cmd, r.ExitCode, truncateHeadTail(redactSecrets(out), 8000)))
	if r.ExitCode != 0 && (a.ol.on || a.ol.forced) {
		a.olOffer("o comando falhou e estou sem modelo — quer:", olShellFailMenu(cmd, out, a.root, a.tools.hasRg))
	}
}

func (a *App) resume(id string) { a.resumeAt(id, "") }

// resumeAt loads a session (id "" = the newest) at an entry of its tree.
func (a *App) resumeAt(id, at string) {
	si, err := pfFindSession(a.root, id)
	if err != nil {
		a.ui.Warn(err.Error())
		return
	}
	a.loadSessionAt(si, at)
}

func (a *App) loadSession(si SessionInfo) { a.loadSessionAt(si, "") }

// loadSessionAt resumes si and renders the conversation again (sessions_ui.go).
func (a *App) loadSessionAt(si SessionInfo, at string) {
	if err := a.agent.LoadSessionAt(si, at); err != nil {
		a.ui.Error("não consegui abrir a sessão: " + err.Error())
		return
	}
	a.started = false
	a.showResumed(si)
}

// newAgent replaces the conversation with a fresh one (keeps tools and MCP).
func (a *App) newAgent() {
	a.agent.Close()
	perms, hooks, loop := a.agent.Perms, a.agent.Hooks, a.agent.LoopOpts()
	a.agent = NewAgent(a.cfg, a.ui, a.tools, a.mcp, a.agent.Model)
	a.agent.Perms, a.agent.Hooks = perms, hooks
	a.agent.SetLoopOpts(loop) // /until and /review outlive /clear; /goal does not
	a.started = false
}

// confirmJobs asks what to do with running background jobs before leaving.
// exiting=true: [s] stop them and leave, [n] leave them running, Esc cancels.
// It returns false when the user cancelled.
func (a *App) confirmJobs(exiting bool) bool {
	n := a.tools.RunningJobs()
	if n == 0 {
		return true
	}
	q := fmt.Sprintf("%d job(s) em segundo plano rodando. Encerrar?", n)
	if exiting {
		switch a.ui.Choose(q, yesNo("Sim, encerrar e sair", "Não, sair e deixar rodando"), 0) {
		case 0:
			a.tools.KillJobs()
			return true
		case 1:
			a.keepJobs = true
			return true
		}
		return false
	}
	switch a.ui.Choose(q, yesNo("Sim, encerrar", "Não, deixar rodando"), 0) {
	case 0:
		a.tools.KillJobs()
		return true
	case 1:
		return true
	}
	return false
}

// ---------- commands ----------

var slashCommands = []struct{ name, help string }{
	{"/help", "esta ajuda"},
	{"/model", "mostra ou troca o modelo: /model anthropic/claude-sonnet-5-5"},
	{"/models", "lista modelos de um provedor: /models openrouter"},
	{"/omni", "roteamento entre provedores: /omni [on|off|strategy free-first|weighted|ordered]"},
	{"/key", "salva uma chave de API (escondida): /key openrouter"},
	{"/login", "login com a conta do ChatGPT (--browser pra fluxo no navegador)"},
	{"/logout", "remove credencial: /logout openai"},
	{"/mode", "permissões: turbo (padrão com sandbox; só pergunta o necessário) · auto (padrão sem sandbox) · plan (só investiga) · ask · smart (modelo rápido decide) · yolo — shift+tab alterna turbo → auto → plan → ask"},
	{"/approvals", "aprovações \"sempre\" deste projeto (saída de dados, fora do projeto, git destrutivo): lista · clear"},
	{"/permissions", "regras de permissão: lista · /permissions remove <n|regra> · add allow|deny|ask <regra>"},
	{"/effort", "esforço de raciocínio: low · medium · high · off"},
	{"/think", "mostra/esconde o raciocínio do modelo"},
	{"/compact", "compacta o contexto agora (opcional: foco do resumo)"},
	{"/context", "o que está ocupando a janela de contexto"},
	{"/undo", "desfaz as mudanças de arquivo do último turno"},
	{"/rewind", "volta a conversa (e os arquivos) para antes de uma mensagem sua"},
	{"/fork", "copia a conversa para uma sessão nova (opcional: até a mensagem n)"},
	{"/tree", "árvore da sessão com ↑↓: enter continua daquele ponto (novo ramo) · /tree <id>"},
	{"/export", "exporta a conversa: /export [md|html] [arquivo]"},
	{"/clear", "começa uma conversa nova"},
	{"/resume", "retoma uma sessão: lista com ↑↓ (digite pra filtrar, tab: todas as pastas) · /resume <id>"},
	{"/sessions", "o mesmo que /resume"},
	{"/rename", "renomeia a sessão: /rename <título> (sem título edita o atual)"},
	{"/script", "scripts prontos (JSON): lista · /script <nome> {\"arg\": valor}"},
	{"/offline", "modo offline: sem modelo (atalhos, /script, !comando) · /online volta"},
	{"/online", "sai do modo offline e tenta os provedores agora"},
	{"/forget", "apaga do disco a sessão atual"},
	{"/worktree", "worktree git isolado desta sessão: info, ou lista os worktrees (jazz --worktree [nome] cria)"},
	{"/redact", "segredos que não saem da máquina: status · on|off|strict · allow <secreto:tipo#n> (falso positivo)"},
	{"/trust", "confia nesta pasta (AGENTS.md, skills, .mcp.json, hooks) e sai do modo ask · /trust status · /trust revoke"},
	{"/todo", "mostra o plano atual"},
	{"/goal", "objetivo fixo da sessão, lembrado a cada turno: /goal <objetivo> · /goal off"},
	{"/until", "só termina o turno quando o comando passar: /until go test ./... · /until off"},
	{"/review", "revisão do diff por um subagente depois de turnos com mudanças: /review on|off"},
	{"/skill", "skills embutidas: lista · /skill <nome> [pedido] aplica uma no próximo turno"},
	{"/jobs", "lista os jobs em segundo plano · /jobs kill"},
	{"/mcp", "status dos servidores MCP · /mcp resources · /mcp prompts"},
	{"/cost", "tokens e custo desta sessão"},
	{"/expand", "mostra a saída inteira da última ferramenta (ctrl+o)"},
	{"/init", "cria um AGENTS.md analisando o projeto"},
	{"/doctor", "diagnóstico"},
	{"/exit", "sair"},
}

func (a *App) completeCmd(prefix string) []string {
	var out []string
	for _, c := range slashCommands {
		if strings.HasPrefix(c.name, prefix) {
			out = append(out, c.name)
		}
	}
	for _, c := range customCommands(a.root) {
		if strings.HasPrefix("/"+c.name, prefix) {
			out = append(out, "/"+c.name)
		}
	}
	return out
}

func (a *App) command(line string) {
	if fixed, menu, note := fzFixCommand(line, a.customNames()); len(menu) > 0 { // typos (fuzzy_cmd.go)
		a.fzOffer(line, menu)
		return
	} else if fixed != "" {
		a.ui.Notice(note)
		line = fixed
	}
	f := strings.Fields(line)
	cmd, args := f[0], f[1:]
	argStr := strings.TrimSpace(strings.TrimPrefix(line, cmd))
	ui := a.ui
	t := ui.T
	if a.olCommand(cmd, argStr) { // /offline, /online, /script (offline.go)
		return
	}
	if a.wtCommand(cmd, argStr) { // /worktree (worktree.go)
		return
	}
	switch cmd {
	case "/help", "/?":
		var b strings.Builder
		b.WriteString("\n")
		w := termWidth() - 1
		// word-wrapped with a hanging indent: a narrow terminal never cuts words
		item := func(color, name, help string) {
			lines := uiWrapWords(help, max(w-15, 20))
			fmt.Fprintf(&b, "  %s%-12s%s %s\n", color, name, t.sgr("0"), strings.Join(lines, "\n"+strings.Repeat(" ", 15)))
		}
		for _, c := range slashCommands {
			item(t.fg(cMagenta, 35), c.name, c.help)
		}
		for _, c := range customCommands(a.root) {
			item(t.fg(cCyan, 36), "/"+c.name, c.desc)
		}
		b.WriteString("\n")
		for _, l := range []string{"!comando roda no shell direto · esc interrompe o agente (com texto digitado: a 1ª limpa) · digitar durante o trabalho entra na fila",
			"shift+enter, alt+enter ou \\ + enter quebra linha · ↑↓ histórico (filtra pelo que já foi digitado) · ctrl+r busca · tab completa · shift+tab ou ctrl+t troca o modo · @ abre a lista de arquivos · ctrl+o expande/recolhe · ? atalhos"} {
			b.WriteString("  " + t.fg(cGray, 90) + strings.Join(uiWrapWords(l, max(w-2, 20)), "\n  ") + t.sgr("0") + "\n")
		}
		b.WriteString("\n")
		ui.print(b.String())
	case "/exit", "/quit", "/q":
		if a.confirmJobs(true) {
			a.quit = true
		}
	case "/model":
		if len(args) == 0 {
			ui.Notice("modelo atual: " + a.agent.Model)
			var have []string
			for _, p := range a.cfg.ProviderNames() {
				mark := "  "
				if a.cfg.HasCreds(p) {
					mark = t.G().ok + " "
				}
				have = append(have, mark+p+"/"+a.cfg.Providers[p].DefaultModel)
			}
			ui.print(t.fg(cGray, 90) + "    " + strings.Join(have, "\n    ") + t.sgr("0") + "\n")
			return
		}
		ref, err := a.cfg.ResolveModel(args[0])
		if err != nil {
			ui.Error(err.Error())
			return
		}
		a.agent.SetModel(ref)
		a.cfg.Model = ref
		safeGo(func() { // per-model window lands in the models cache (providers), never in the global config (bug 42)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			_, _ = DetectWindow(ctx, a.cfg, ref)
		})
		if err := a.cfg.Save(); err != nil {
			ui.Warn("não salvei como padrão: " + err.Error())
		}
		ui.Notice("modelo: " + ref + " (salvo como padrão)")
		if !a.cfg.HasCreds(mustProvider(ref)) {
			ui.Warn("sem credencial pra " + mustProvider(ref) + " — /key " + mustProvider(ref))
		}
	case "/models":
		p := mustProvider(a.agent.Model)
		if len(args) > 0 {
			p = args[0]
		}
		var b strings.Builder
		cmdModels(a.cfg, p, &b, t)
		ui.print(b.String())
	case "/omni":
		out, err := pfOmniCommand(a.cfg, a.agent.Model, args)
		if err != nil {
			ui.Error(err.Error())
			return
		}
		ui.print(t.fg(cGray, 90) + pfIndentLines(out, "  ") + t.sgr("0"))
	case "/key":
		if len(args) == 0 {
			ui.Error("uso: /key <provedor>   (" + strings.Join(a.cfg.ProviderNames(), ", ") + ")")
			return
		}
		cmdKey(a.cfg, args[0], a)
	case "/login":
		ctx, cancel := context.WithCancel(context.Background())
		stop := ui.WatchTurn(cancel, nil)
		browser := strings.Contains(argStr, "--browser")
		var err error
		say := func(s string) { ui.print(s) }
		if browser {
			err = LoginChatGPTBrowser(ctx, say)
		} else {
			err = LoginChatGPTDevice(ctx, say)
		}
		stop()
		cancel()
		if err != nil {
			ui.Error("login falhou: " + err.Error())
			return
		}
		refreshSecrets()
		ui.Notice("logado no ChatGPT " + t.G().ok + "  use: /model chatgpt")
	case "/logout":
		if len(args) == 0 {
			ui.Error("uso: /logout <provedor>")
			return
		}
		_ = SaveCred(args[0], nil)
		ui.Notice("credencial de " + args[0] + " removida")
	case "/mode":
		if len(args) == 0 {
			ui.Notice("modo: " + a.cfg.EffMode() + "  (auto · turbo · smart · ask · yolo · plan)")
			return
		}
		args[0] = tbModeName(args[0])
		switch args[0] {
		case "auto", "smart", "ask", "yolo", "turbo":
			if args[0] == "yolo" && !turboSandboxOK() && !pfConfirmYoloWithoutSandbox() {
				ui.Notice("modo não alterado")
				return
			}
			a.cfg.SetMode(args[0])
			_ = a.cfg.Save()
			ui.SetMode(args[0])
			switch args[0] {
			case "smart":
				ui.Notice(smModeNotice)
			case "turbo":
				ui.Notice(turboModeNotice())
			default:
				ui.Notice("modo: " + args[0])
			}
		case "plan":
			// plan is session-only: never written to config (bug 9)
			a.cfg.SetMode("plan")
			ui.SetMode("plan")
			ui.Notice("modo plan: o agente só investiga e propõe um plano (/mode auto pra executar)")
		default:
			ui.Error("modos: auto · turbo · smart · ask · yolo · plan")
		}
	case "/permissions", "/perms":
		a.permissionsCmd(args)
	case "/approvals", "/aprovacoes", "/aprovações":
		a.approvalsCmd(args)
	case "/trust":
		a.trustCmd(args)
	case "/redact", "/segredos":
		a.redactCmd(args)
	case "/effort":
		if len(args) == 0 {
			e := a.cfg.Effort
			if e == "" {
				e = "padrão do modelo"
			}
			ui.Notice("esforço: " + e)
			return
		}
		e := args[0]
		if e == "off" || e == "default" {
			e = ""
		}
		a.cfg.Effort = e
		_ = a.cfg.Save()
		ui.Notice("esforço de raciocínio: " + args[0])
	case "/think":
		a.cfg.ShowThinking = !a.cfg.ShowThinking
		_ = a.cfg.Save()
		ui.Notice(fmt.Sprintf("mostrar raciocínio: %v", a.cfg.ShowThinking))
	case "/compact":
		ctx, cancel := context.WithCancel(context.Background())
		stop := ui.WatchTurn(cancel, nil)
		a.ensureStarted(ctx)
		err := a.agent.Compact(ctx, argStr)
		stop()
		cancel()
		if err != nil {
			ui.Error(err.Error())
		}
	case "/context":
		a.contextCmd()
	case "/undo":
		paths, err := a.tools.Undo()
		if err != nil {
			ui.Error("undo falhou: " + err.Error())
		}
		if len(paths) == 0 {
			if a.tools.BashTracked() {
				ui.Notice("nada pra desfazer")
			} else {
				ui.Notice("nada pra desfazer (mudanças feitas via bash não são rastreadas)")
			}
			return
		}
		ui.Notice("restaurado: " + strings.Join(paths, ", "))
		a.agent.Note("<system-reminder>The user reverted (/undo) your last changes to: " + strings.Join(paths, ", ") + ". Those files are back to their previous content.</system-reminder>")
	case "/rewind":
		turns := a.agent.UserTurns()
		if len(turns) == 0 {
			ui.Notice("nenhuma mensagem sua pra voltar")
			return
		}
		i := a.pickTurn(turns, "Voltar para antes de qual mensagem?")
		if i < 0 {
			return
		}
		n, tr := i+1, turns[i]
		if err := a.agent.RewindTo(tr.Index); err != nil {
			ui.Error("rewind falhou: " + err.Error())
			return
		}
		a.ed.Prefill(tr.Text)
		if a.tools.BashTracked() {
			ui.Notice(fmt.Sprintf("conversa voltou para antes da mensagem %d (arquivos alterados depois dela foram restaurados, inclusive via bash)", n))
		} else {
			ui.Notice(fmt.Sprintf("conversa voltou para antes da mensagem %d (arquivos alterados depois dela foram restaurados; bash não é rastreado)", n))
		}
	case "/fork":
		at := len(a.agent.Msgs)
		if len(args) > 0 {
			turns := a.agent.UserTurns()
			var n int
			if _, err := fmt.Sscan(args[0], &n); err != nil || n < 1 || n > len(turns) {
				ui.Error(fmt.Sprintf("uso: /fork [n]  (n de 1 a %d, contando suas mensagens desde o início)", len(turns)))
				return
			}
			at = turns[n-1].Index
		}
		old := a.agent.SessionID
		id, err := a.agent.Fork(at)
		if err != nil {
			ui.Error("fork falhou: " + err.Error())
			return
		}
		ui.Notice("nova sessão " + id + " (a anterior continua em jazz -r " + old + ")")
	case "/tree":
		a.treeCmd(args)
	case "/export":
		a.exportCmd(args)
	case "/clear", "/new":
		if !a.confirmJobs(false) {
			return
		}
		a.newAgent()
		a.tools.Reset()
		a.tools.Shell.Close()
		ui.print("\x1b[H\x1b[2J" + a.welcome())
	case "/resume", "/sessions": // sessions_ui.go
		a.resumeCmd(args)
	case "/rename":
		a.renameCmd(argStr)
	case "/forget":
		id := a.agent.SessionID
		if id == "" {
			ui.Notice("esta conversa ainda não foi salva")
			return
		}
		if a.ui.Choose("Apagar do disco a sessão "+id+"?", yesNo("Sim, apagar", "Não"), 0) != 0 {
			return
		}
		p := filepath.Join(sessionsDir(a.root), id+".jsonl")
		a.newAgent()
		a.tools.Reset()
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			ui.Error("não consegui apagar: " + err.Error())
			return
		}
		ui.Notice("sessão " + id + " apagada; começando uma conversa nova")
	case "/goal", "/until", "/review": // main_loop.go
		a.loopCmd(cmd, argStr)
	case "/skill", "/skills": // main_skill.go
		a.skillCmd(argStr)
	case "/todo":
		todos := a.tools.TodoList()
		if len(todos) == 0 {
			ui.Notice("sem plano no momento")
			return
		}
		ui.ToolEnd("", "todo", ToolOutput{Display: Display{Summary: "plano", Todos: todos}}, 0)
	case "/jobs":
		if len(args) > 0 && args[0] == "kill" {
			ui.Notice(fmt.Sprintf("%d job(s) encerrado(s)", a.tools.KillJobs()))
			ui.Jobs(a.tools.RunningJobs())
			return
		}
		out := a.tools.Exec(context.Background(), "bash", `{"action":"list"}`)
		ui.print(t.fg(cGray, 90) + pfIndentLines(out.Text, "  ") + t.sgr("0"))
	case "/mcp":
		a.mcpCmd(args)
	case "/expand":
		ui.ExpandLast()
	case "/cost", "/usage":
		u := a.agent.Usage
		msg := fmt.Sprintf("sessão: entrada %s · saída %s · cache lido %s · escrito %s", fmtK(u.In), fmtK(u.Out), fmtK(u.CacheRead), fmtK(u.CacheWrite))
		if u.Cost > 0 {
			msg += fmt.Sprintf(" · US$ %.4f", u.Cost)
		}
		ui.Notice(msg)
	case "/init":
		a.send(initPrompt)
	case "/doctor":
		cmdDoctor(a.cfg, a.root, nil, false)
	default:
		for _, c := range customCommands(a.root) {
			if "/"+c.name == cmd {
				body := strings.ReplaceAll(c.body, "$ARGUMENTS", argStr)
				if !strings.Contains(c.body, "$ARGUMENTS") && argStr != "" {
					body += "\n\n" + argStr
				}
				a.send(body)
				return
			}
		}
		ui.Error("comando desconhecido: " + cmd + " (veja /help)")
	}
}

func (a *App) mcpCmd(args []string) {
	ui, t := a.ui, a.ui.T
	if len(args) > 0 && (args[0] == "resources" || args[0] == "prompts") {
		var b strings.Builder
		for _, l := range a.mcp.mcpCatalogLines(args[0]) {
			b.WriteString("  " + t.fg(cGray, 90) + sanitizeTerm(l) + t.sgr("0") + "\n")
		}
		ui.print(b.String())
		return
	}
	if len(a.mcp.Servers) == 0 {
		ui.Notice("nenhum servidor MCP. Configure em " + filepath.Join(configDir(), "mcp.json") + " ou .mcp.json no projeto (formato mcpServers do Claude Code)")
		return
	}
	var b strings.Builder
	for _, s := range a.mcp.Servers {
		st, serr, ntools := s.State()
		col := t.fg(cGreen, 32)
		if st != "ready" {
			col = t.fg(cGold, 33)
		}
		if st == "failed" {
			col = t.fg(cRed, 31)
		}
		origin := ""
		if s.Project {
			origin = " (projeto)"
		}
		fmt.Fprintf(&b, "  %s%s%s %s%s %s(%d tools)%s", col, st, t.sgr("0"), sanitizeTerm(s.Name), origin, t.fg(cGray, 90), ntools, t.sgr("0"))
		if serr != nil {
			fmt.Fprintf(&b, " %s%s%s", t.fg(cRed, 31), sanitizeTerm(serr.Error()), t.sgr("0"))
		}
		b.WriteString("\n")
		if a.mcp.Done() { // tools are complete only after connecting
			for _, n := range s.ToolNames() {
				hidden := ""
				if !pfToolAllowed(s.Conf, n) {
					hidden = " (desativada)"
				}
				fmt.Fprintf(&b, "      %s%s%s%s\n", t.fg(cGray, 90), sanitizeTerm(n), hidden, t.sgr("0"))
			}
		}
	}
	ui.print(b.String())
}

func (a *App) permissionsCmd(args []string) {
	ui, t := a.ui, a.ui.T
	perms := a.agent.Perms
	if perms == nil {
		perms = LoadPerms(a.cfg, a.root, a.opts.allowTools)
		a.agent.Perms = perms
	}
	if len(args) == 0 || args[0] == "list" {
		rules := perms.Rules()
		if len(rules) == 0 {
			ui.Notice("nenhuma regra. Escolha \"Sim, e não perguntar de novo\" numa confirmação, ou: /permissions add allow 'Bash(npm test:*)'")
			return
		}
		var b strings.Builder
		for i, r := range rules {
			col := t.fg(cGreen, 32)
			switch r.Kind {
			case "deny":
				col = t.fg(cRed, 31)
			case "ask":
				col = t.fg(cGold, 33)
			}
			src := r.Source
			if rel, err := filepath.Rel(a.root, src); err == nil && !strings.HasPrefix(rel, "..") {
				src = rel
			}
			fmt.Fprintf(&b, "  %s%2d%s %s%-5s%s %s %s%s%s\n", t.fg(cMagenta, 35), i+1, t.sgr("0"), col, r.Kind, t.sgr("0"), sanitizeTerm(r.Rule), t.fg(cGray, 90), src, t.sgr("0"))
		}
		b.WriteString("  " + t.fg(cGray, 90) + "deny vence ask, ask vence allow · comando perigoso sempre pergunta · /permissions remove <n>" + t.sgr("0") + "\n")
		ui.print(b.String())
		return
	}
	switch args[0] {
	case "remove", "rm", "del":
		if len(args) < 2 {
			ui.Error("uso: /permissions remove <n|regra>")
			return
		}
		rule := strings.Join(args[1:], " ")
		var n int
		if _, err := fmt.Sscan(rule, &n); err == nil && fmt.Sprint(n) == rule {
			rules := perms.Rules()
			if n < 1 || n > len(rules) {
				ui.Error("número inválido")
				return
			}
			rule = rules[n-1].Rule
		}
		if err := perms.Remove(rule); err != nil {
			ui.Error(err.Error())
			return
		}
		ui.Notice("regra removida: " + rule)
	case "add":
		if len(args) < 3 || (args[1] != "allow" && args[1] != "deny" && args[1] != "ask") {
			ui.Error("uso: /permissions add allow|deny|ask <regra>   ex.: Bash(git push:*) · Edit(.env) · mcp__github__*")
			return
		}
		rule := strings.Trim(strings.Join(args[2:], " "), `"'`)
		if err := perms.pfAdd(args[1], rule); err != nil {
			ui.Error(err.Error())
			return
		}
		ui.Notice(args[1] + " " + pfNormRule(rule) + " salvo em .jazz/settings.json")
	default:
		ui.Error("uso: /permissions [list | remove <n|regra> | add allow|deny|ask <regra>]")
	}
}

func (a *App) contextCmd() {
	a.ensureStarted(context.Background())
	// the UI renders the breakdown ("total" row overrides the sum; rows whose
	// label starts with a space are details and are not summed)
	a.ui.ShowContext(a.agent.ContextBreakdown(), a.agent.Window())
}

func (a *App) exportCmd(args []string) {
	format, out := "md", ""
	for _, x := range args {
		switch x {
		case "md", "markdown":
			format = "md"
		case "html":
			format = "html"
		default:
			out = x
		}
	}
	if len(a.agent.Msgs) == 0 {
		a.ui.Notice("conversa vazia, nada pra exportar")
		return
	}
	doc, err := a.agent.Export(format)
	if err != nil {
		a.ui.Error("export falhou: " + err.Error())
		return
	}
	if out == "" {
		id := a.agent.SessionID
		if id == "" {
			id = time.Now().Format("060102-150405")
		}
		out = filepath.Join(a.root, "jazz-"+id+"."+format)
	}
	if err := os.WriteFile(out, []byte(redactSecrets(doc)), 0o644); err != nil {
		a.ui.Error(err.Error())
		return
	}
	a.ui.Notice("exportado: " + out)
}

const initPrompt = `Analyze this project and create (or improve, if it exists) an AGENTS.md file at the project root for future coding agents. Explore with rg/fd/ls and read the key files first. Include, concisely (under ~80 lines):
- what the project is and its main directories
- exact commands to build, test (including a single test), lint and run
- code style and conventions actually used here
- gotchas a newcomer would hit
Do not invent commands: verify they exist (package.json scripts, Makefile, go.mod, pyproject, etc.).`

type customCmd struct{ name, desc, body string }

func customCommands(root string) []customCmd {
	dirs := []string{filepath.Join(configDir(), "commands"), filepath.Join(root, ".jazz", "commands"), filepath.Join(root, ".claude", "commands")}
	seen := map[string]bool{}
	var out []customCmd
	for _, d := range dirs {
		ents, _ := os.ReadDir(d)
		for _, e := range ents {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			name := strings.TrimSuffix(e.Name(), ".md")
			if seen[name] {
				continue
			}
			p := filepath.Join(d, e.Name())
			within := ""
			if strings.HasPrefix(d, root+string(filepath.Separator)) {
				within = root // project commands: regular files inside the project only (F6)
			}
			b, ok := pfSafeRead(p, within, 1<<20)
			if !ok {
				continue
			}
			body := redactSecrets(string(b))
			desc := ""
			if strings.HasPrefix(body, "---") {
				if end := strings.Index(body[3:], "\n---"); end >= 0 {
					for _, l := range strings.Split(body[3:3+end], "\n") {
						if k, v, ok := strings.Cut(l, ":"); ok && strings.TrimSpace(k) == "description" {
							desc = strings.Trim(strings.TrimSpace(v), `"'`)
						}
					}
					body = strings.TrimSpace(body[3+end+4:])
				}
			}
			if desc == "" {
				desc = oneLine(body, 60)
			}
			seen[name] = true
			out = append(out, customCmd{name, desc, body})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// ---------- subcommands shared by CLI and REPL ----------

func cmdLogin(cfg *Config, args []string) {
	browser := false
	for _, a := range args {
		if a == "--browser" {
			browser = true
		}
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	say := func(s string) { fmt.Print(s) }
	var err error
	if browser {
		err = LoginChatGPTBrowser(ctx, say)
	} else {
		err = LoginChatGPTDevice(ctx, say)
	}
	if err != nil {
		fatal("login falhou: " + err.Error())
	}
	fmt.Println("\n  ✓ logado no ChatGPT. Use: jazz -m chatgpt   (ou /model chatgpt)")
	if cfg.Model == "" {
		cfg.Model = "chatgpt/" + cfg.Providers["chatgpt"].DefaultModel
		_ = cfg.Save()
	}
}

// pfMaskKey shows only enough of a key to recognise it (bug 62): the last 4
// characters for long keys, nothing for short ones.
func pfMaskKey(key string) string {
	if len(key) >= 12 {
		return "…" + key[len(key)-4:]
	}
	return strings.Repeat("•", 8)
}

// cmdKey reads an API key without echo (or from stdin if piped) and stores it.
func cmdKey(cfg *Config, provider string, app *App) {
	pc, ok := cfg.Providers[provider]
	if !ok {
		msg := "provedor desconhecido: " + provider + " (" + strings.Join(cfg.ProviderNames(), ", ") + ")"
		if app != nil {
			app.ui.Error(msg)
			return
		}
		fatal(msg)
	}
	if pc.Type == "codex" {
		msg := "chatgpt usa login, não chave: jazz login"
		if app != nil {
			app.ui.Error(msg)
			return
		}
		fatal(msg)
	}
	var key string
	if app != nil {
		// cbreak mode is active and echo is off: read keys, show bullets
		app.ui.print(fmt.Sprintf("  cole a chave de %s (fica escondida) %s ", provider, app.ui.T.G().prompt))
		var buf []rune
	loop:
		for k := range app.ui.keys.Keys {
			switch k.Code {
			case KEnter:
				break loop
			case KEsc, KCtrlC:
				app.ui.print("\n")
				app.ui.Notice("cancelado")
				return
			case KBackspace:
				if len(buf) > 0 {
					buf = buf[:len(buf)-1]
					app.ui.print("\b \b")
				}
			case KRune:
				buf = append(buf, k.R)
				app.ui.print("•")
			case KPaste:
				buf = append(buf, []rune(k.Text)...)
				app.ui.print(strings.Repeat("•", len([]rune(k.Text))))
			}
		}
		app.ui.print("\n")
		key = string(buf)
	} else if isTTY(os.Stdin) {
		fmt.Printf("cole a chave de %s (fica escondida): ", provider)
		restore, err := enterCbreak(int(os.Stdin.Fd()))
		if err == nil {
			br := bufio.NewReader(os.Stdin)
			var b []byte
			for {
				c, err := br.ReadByte()
				if err != nil || c == '\r' || c == '\n' {
					break
				}
				if c == 0x03 {
					restore()
					fmt.Println()
					os.Exit(130)
				}
				if c == 0x7f || c == 0x08 {
					if len(b) > 0 {
						b = b[:len(b)-1]
					}
					continue
				}
				if c >= 0x20 {
					b = append(b, c)
				}
			}
			restore()
			key = strings.ReplaceAll(strings.ReplaceAll(string(b), "\x1b[200~", ""), "\x1b[201~", "")
		}
		fmt.Println()
	} else {
		b, _ := io.ReadAll(io.LimitReader(os.Stdin, 64<<10))
		key = string(b)
	}
	key = strings.TrimSpace(key)
	if key == "" {
		if app != nil {
			app.ui.Notice("nenhuma chave digitada")
			return
		}
		fatal("nenhuma chave recebida")
	}
	if w := KeyFormatWarning(cfg, provider, key); w != "" {
		if app != nil {
			app.ui.Warn(w)
		} else {
			fmt.Fprintln(os.Stderr, "  aviso:", w)
		}
	}
	cred := &Cred{Type: "api", Key: key}
	if pc.Account && pc.AccountEnv != "" {
		// Cloudflare-style URLs need an account id too: keep it with the key
		if id := strings.TrimSpace(os.Getenv(pc.AccountEnv)); id != "" {
			cred.AccountID = id
		} else if cfg.AccountID(provider) == "" {
			msg := provider + " também precisa do id da conta: exporte " + pc.AccountEnv + " e rode jazz key " + provider + " de novo"
			if app != nil {
				app.ui.Warn(msg)
			} else {
				fmt.Fprintln(os.Stderr, "  aviso:", msg)
			}
		}
	}
	if err := SaveCred(provider, cred); err != nil {
		if app != nil {
			app.ui.Error(err.Error())
			return
		}
		fatal(err.Error())
	}
	refreshSecrets() // the new key is redacted from tool output from now on
	msg := fmt.Sprintf("chave de %s salva (%s) em %s", provider, pfMaskKey(key), authPath())
	if app != nil {
		app.ui.Notice(msg)
		if !app.cfg.HasCreds(mustProvider(app.agent.Model)) {
			ref := provider + "/" + pc.DefaultModel
			app.agent.SetModel(ref)
			app.cfg.Model = ref
			_ = app.cfg.Save()
			app.ui.Notice("modelo agora: " + ref)
		}
		return
	}
	fmt.Println("  ✓", msg)
	if cfg.Model == "" || !cfg.HasCreds(mustProvider(cfg.Model)) {
		cfg.Model = provider + "/" + pc.DefaultModel
		_ = cfg.Save()
		fmt.Println("  modelo padrão:", cfg.Model)
	}
}

func cmdModels(cfg *Config, provider string, w io.Writer, t Theme) {
	if provider == "" {
		provider = "openrouter"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ms, err := ListModels(ctx, cfg, provider)
	if err != nil {
		fmt.Fprint(w, uiErrorLine(t, friendlyErr(err), termWidth()-1))
		return
	}
	shown := 0
	for _, m := range ms {
		if !m.Tools {
			continue
		}
		tag := ""
		if m.Free {
			tag = t.fg(cGreen, 32) + " grátis" + t.sgr("0")
		}
		ctxs := ""
		if m.Ctx > 0 {
			ctxs = t.fg(cGray, 90) + " " + fmtK(m.Ctx) + t.sgr("0")
		}
		fmt.Fprintf(w, "  %s/%s%s%s\n", provider, m.ID, ctxs, tag)
		shown++
		if shown >= 80 {
			fmt.Fprintf(w, "  %s… e mais %d%s\n", t.fg(cGray, 90), len(ms)-shown, t.sgr("0"))
			break
		}
	}
	if shown == 0 {
		fmt.Fprintln(w, "  nenhum modelo com suporte a ferramentas")
	}
}

// cmdProvider implements `jazz provider add <nome> --url URL [...]` and `jazz provider list`.
func cmdProvider(cfg *Config, args []string) {
	if len(args) == 0 || args[0] == "list" || args[0] == "ls" {
		for _, p := range cfg.ProviderNames() {
			pc := cfg.Providers[p]
			fmt.Printf("  %-11s %-9s %s  %s\n", p, pc.Type, pc.BaseURL, pc.DefaultModel)
		}
		return
	}
	if args[0] != "add" || len(args) < 2 || strings.HasPrefix(args[1], "-") {
		pfUsageErr("uso: jazz provider add <nome> --url URL [--type openai|anthropic|gemini] [--key-env VAR] [--model M] [--header K=V] [--no-key] [--ollama]")
	}
	name := args[1]
	fs := flag.NewFlagSet("provider add", flag.ContinueOnError)
	u := fs.String("url", "", "")
	typ := fs.String("type", "openai", "")
	keyEnv := fs.String("key-env", "", "")
	model := fs.String("model", "", "")
	noKey := fs.Bool("no-key", false, "")
	ollama := fs.Bool("ollama", false, "")
	var headers pfHeaderFlag
	fs.Var(&headers, "header", "")
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args[2:]); err != nil {
		pfUsageErr(err.Error())
	}
	if *u == "" {
		pfUsageErr("falta --url (ex.: http://127.0.0.1:11434/v1 para Ollama)")
	}
	pc := ProviderConf{Type: *typ, BaseURL: strings.TrimSuffix(*u, "/"), KeyEnv: *keyEnv, DefaultModel: *model, NoKey: *noKey, Ollama: *ollama}
	if len(headers) > 0 {
		pc.Headers = map[string]string(headers)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ms, err := cfg.AddProvider(ctx, name, pc)
	if err != nil {
		fatal("não consegui adicionar " + name + ": " + err.Error())
	}
	if cfg.Providers[name] == nil { // providers' AddProvider normally saves it already
		cfg.Providers[name] = &pc
		if err := cfg.Save(); err != nil {
			fatal(err.Error())
		}
	}
	fmt.Printf("  ✓ provedor %s salvo (%s, %d modelos encontrados)\n", name, pc.BaseURL, len(ms))
	for i, m := range ms {
		if i >= 15 {
			fmt.Printf("  … e mais %d\n", len(ms)-i)
			break
		}
		fmt.Printf("    %s/%s\n", name, m.ID)
	}
	if !pc.NoKey && pc.KeyEnv == "" {
		fmt.Printf("  chave: jazz key %s  (ou --no-key para servidores locais)\n", name)
	}
	fmt.Printf("  use: jazz -m %s/<modelo>\n", name)
}

// pfHeaderFlag collects repeated --header K=V.
type pfHeaderFlag map[string]string

func (h *pfHeaderFlag) String() string { return "" }
func (h *pfHeaderFlag) Set(v string) error {
	k, val, ok := strings.Cut(v, "=")
	if !ok {
		k, val, ok = strings.Cut(v, ":")
	}
	if !ok || strings.TrimSpace(k) == "" {
		return errors.New("--header espera K=V")
	}
	if *h == nil {
		*h = map[string]string{}
	}
	(*h)[strings.TrimSpace(k)] = strings.TrimSpace(val)
	return nil
}

// cmdExport implements `jazz export <id> [md|html] [arquivo]`.
func cmdExport(cfg *Config, root string, args []string) {
	if len(args) == 0 {
		pfUsageErr("uso: jazz export <id-da-sessão> [md|html] [arquivo]   (ids: /resume no modo interativo)")
	}
	si, err := pfFindSession(root, args[0])
	if err != nil {
		fatal(err.Error())
	}
	format, out := "md", ""
	for _, x := range args[1:] {
		switch x {
		case "md", "markdown":
			format = "md"
		case "html":
			format = "html"
		default:
			out = x
		}
	}
	ag := NewAgent(cfg, &HeadlessUI{}, NewToolbox(root), nil, cfg.Model)
	if err := ag.LoadSession(si); err != nil {
		fatal(err.Error())
	}
	defer ag.Close()
	doc, err := ag.Export(format)
	if err != nil {
		fatal(err.Error())
	}
	doc = redactSecrets(doc)
	if out == "-" {
		fmt.Print(doc)
		return
	}
	if out == "" {
		out = "jazz-" + si.ID + "." + format
	}
	if err := os.WriteFile(out, []byte(doc), 0o644); err != nil {
		fatal(err.Error())
	}
	fmt.Println("exportado:", out)
}

// cmdDoctor: jazz doctor [-v|--debug] [--sandbox-probe]. -v prints the
// sandbox wrapper argv; --sandbox-probe prints every path the live sandbox
// check tried and what it got.
func cmdDoctor(cfg *Config, root string, args []string, verbose bool) {
	probe := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch strings.TrimLeft(a, "-") {
		case "v", "verbose", "debug":
			verbose = true
		case "sandbox-probe", "probe":
			probe = true
		case "provider", "p":
			if i+1 >= len(args) {
				pfUsageErr("uso: jazz doctor --provider <provedor>[/modelo]   (ex.: anthropic, anthropic/claude-sonnet-5-5)")
			}
			os.Exit(pfDoctorProvider(cfg, args[i+1], os.Stdout))
		}
		if v, ok := strings.CutPrefix(a, "--provider="); ok {
			os.Exit(pfDoctorProvider(cfg, v, os.Stdout))
		}
	}
	t := DetectTheme()
	g := t.G()
	ok := func(b bool) string {
		if b {
			return t.fg(cGreen, 32) + g.ok + t.sgr("0")
		}
		return t.fg(cRed, 31) + g.fail + t.sgr("0")
	}
	fmt.Printf("\n  jazz-cli %s · %s · %d CPU · %d MB\n", version, osName(), cpuCount(), memTotalMB())
	fmt.Printf("  config  %s\n  dados   %s\n", configDir(), dataDir())
	if exe, err := os.Executable(); err == nil {
		fmt.Printf("  binário %s\n", exe)
	}
	fmt.Println()
	have := map[string]bool{}
	bins := []string{"bash", "rg", "git", "fd", "fdfind", "python3", "setsid"}
	if runtime.GOOS == "windows" {
		fmt.Printf("  shell   %s (%s)\n", detectShell().Name(), detectShell().Path)
		bins = []string{"bash", "pwsh", "rg", "git", "fd", "python"}
	}
	for _, bin := range bins {
		_, err := lookPath(bin)
		have[bin] = err == nil
		fmt.Printf("  %s %s\n", ok(err == nil), bin)
	}
	var missing []string
	if !have["rg"] {
		missing = append(missing, "ripgrep")
	}
	if !have["fd"] && !have["fdfind"] {
		missing = append(missing, "fd-find")
	}
	if len(missing) > 0 && runtime.GOOS == "windows" {
		fmt.Printf("  %sdica: winget install BurntSushi.ripgrep.MSVC sharkdp.fd Git.Git (o Git traz o Git Bash, que o jazz prefere ao PowerShell)%s\n", t.fg(cGold, 33), t.sgr("0"))
	} else if len(missing) > 0 {
		fmt.Printf("  %sdica: %s (o jazz usa grep/find enquanto isso)%s\n", t.fg(cGold, 33), installHint(missing), t.sgr("0"))
	} else if !have["fd"] && have["fdfind"] {
		fmt.Printf("  %sfd → fdfind: o shell do jazz já cria o atalho fd%s\n", t.fg(cGray, 90), t.sgr("0"))
	}
	if p := dgSemgrepPath(); p != "" {
		fmt.Printf("  %s semgrep (revisão do diff: p/security-audit e p/secrets) %s\n", ok(true), t.fg(cGray, 90)+p+t.sgr("0"))
	} else {
		fmt.Printf("  %s semgrep %s\n", t.fg(cGray, 90)+"·"+t.sgr("0"), t.fg(cGray, 90)+"(opcional, regras extras na revisão do diff: pip install semgrep)"+t.sgr("0"))
	}
	sbDoctor(ok, func(s string) string { return t.fg(cGray, 90) + s + t.sgr("0") }, verbose, probe)
	fmt.Println()
	for _, p := range cfg.ProviderNames() {
		fmt.Printf("  %s %-11s %s\n", ok(cfg.HasCreds(p)), p, t.fg(cGray, 90)+cfg.Providers[p].DefaultModel+t.sgr("0"))
	}
	m := cfg.Model
	if m == "" {
		m = cfg.PickDefaultModel() + " (automático)"
	}
	fmt.Printf("\n  modelo padrão: %s · modo: %s · omni: %v\n", m, cfg.BaseMode(), cfg.Omni.Enabled)
	fmt.Println("  " + olDoctorLine(cfg, root)) // scripts, fast paths, offline (offline.go)
	if w := AuthWarning(); w != "" {
		fmt.Printf("  %s%s%s\n", t.fg(cRed, 31), w, t.sgr("0"))
	}
	if r, err := cfg.ResolveModel(strings.TrimSuffix(m, " (automático)")); err == nil && !isOmniRef(r) {
		if p := mustProvider(r); cfg.HasCreds(p) {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			reach, win, err := ProbeProvider(ctx, cfg, p)
			cancel()
			switch {
			case err != nil:
				fmt.Printf("  %s %s: %s\n", ok(false), p, firstSentence(err.Error()))
			case reach:
				extra := ""
				if win > 0 {
					extra = fmt.Sprintf(" (janela %dk)", win/1000)
				}
				fmt.Printf("  %s %s responde%s\n", ok(true), p, extra)
			}
			if w := cfg.ModelWarning(r); w != "" {
				fmt.Printf("  %s%s%s\n", t.fg(cGold, 33), w, t.sgr("0"))
			}
		}
	}
	conf := loadMCPConfig(root)
	fmt.Printf("  servidores MCP configurados: %d\n", len(conf))
	if _, kinds := pfTrustSummary(root); len(kinds) > 0 {
		fmt.Printf("  %sprojeto com configuração não confiável (%s): jazz trust%s\n", t.fg(cGold, 33), strings.Join(kinds, ", "), t.sgr("0"))
	}
	perms := LoadPerms(cfg, root, nil)
	hooks := LoadHooks(cfg, root, pfProjectPolicyTrusted(root))
	nh := 0
	for _, ev := range []string{HookPreToolUse, HookPostToolUse, HookUserPromptSubmit, HookStop, HookSessionStart} {
		nh += len(hooks.byEvent[ev])
	}
	fmt.Printf("  regras de permissão: %d · hooks: %d · sessões guardadas por %d dias\n", len(perms.Rules()), nh, pfSessionDays())
	th := "16 cores"
	if t.truecolor {
		th = "truecolor"
	} else if t.c256 {
		th = "256 cores"
	}
	if t.ascii && runtime.GOOS == "windows" {
		th += ", símbolos ASCII (console sem Windows Terminal; JAZZ_ASCII=0 força Unicode)"
	} else if t.ascii {
		th += ", símbolos ASCII (console Linux)"
	}
	if runtime.GOOS == "windows" && !termVT() {
		th = "sem cores: o console não aceitou sequências VT (use o Windows Terminal)"
	}
	fmt.Printf("  terminal: %s (TERM=%s)\n", th, os.Getenv("TERM"))
	if p := pfLatestLog(); p != "" {
		fmt.Printf("  último log de debug: %s\n", p)
	}
	if n := pfUpdateNotice(cfg); n != "" {
		fmt.Printf("  %s%s%s\n", t.fg(cGold, 33), n, t.sgr("0"))
	}
	fmt.Println()
}

// treeCmd: /tree lists the session tree; /tree <id> moves the leaf there (a
// new branch starts with the next message; files on disk are not touched).
func (a *App) treeCmd(args []string) {
	ui := a.ui
	if a.agent.sess == nil {
		ui.Notice("esta conversa ainda não foi salva")
		return
	}
	if len(args) == 0 {
		a.treePick() // sessions_ui.go
		return
	}
	a.treeGo(args[0])
}
