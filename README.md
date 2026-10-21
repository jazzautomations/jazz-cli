# jazz-cli

```
     ██╗ █████╗ ███████╗███████╗
     ██║██╔══██╗╚══███╔╝╚══███╔╝
     ██║███████║  ███╔╝   ███╔╝
██   ██║██╔══██║ ███╔╝   ███╔╝
╚█████╔╝██║  ██║███████╗███████╗
 ╚════╝ ╚═╝  ╚═╝╚══════╝╚══════╝
```

*[English](README.en.md)*

O jazz é um agente de código para o terminal, escrito em Go: um binário estático só (~11 MB, sem
dependências) que lê arquivos, roda comandos num shell persistente, edita código e verifica o que
fez, falando com o modelo que você escolher (Anthropic, OpenAI, ChatGPT pela sua conta, OpenRouter,
Groq, Gemini, Ollama/llama.cpp e mais uma dúzia de provedores grátis). Ele foi feito para funcionar
bem com modelos **grátis e fracos** e em máquina **velha**: roda num Mac M-series e também num
notebook Pentium M de 32 bits com 2 GB de RAM (um Dell Latitude, linux/386). Para compensar o
modelo fraco, o harness cobra o que um prompt só pediria: orçamento de passos, testes rodados de
verdade antes de dizer "pronto", guardas contra ação fingida e um sandbox do sistema por baixo de
tudo. É um projeto jovem (v0.7), feito por uma pessoa com agentes de IA; espere arestas. Bugs e
lacunas conhecidos estão em [`FINDINGS.md`](FINDINGS.md).

Princípios (detalhes em [`DESIGN.md`](DESIGN.md)):

- Poucas ferramentas (`read`, `write`, `edit`, `bash`, `todo`, `fetch`, `task`, `script`) e um
  prompt pequeno, que funciona até com modelo grátis e contexto curto.
- Prompt de sistema e schemas fixos durante a sessão, pra aproveitar o cache de prompt; lembretes
  vão na mensagem do usuário ou no resultado da ferramenta.
- Interface leve (editor de linha + ANSI), sem flicker por SSH.
- Comando perigoso (rm -rf amplo, push --force, curl | sh, sudo…) sempre pede confirmação.

## Instalação

Binários para linux (386, amd64, arm64), macOS (amd64, arm64) e Windows (amd64, arm64) saem das
[releases](https://github.com/jazzautomations/jazz-cli/releases). Cada release traz um
`manifest.txt` com a versão e o sha256 de cada binário, assinado com Ed25519
(`manifest.txt.sig`); os instaladores e o `jazz update` conferem os dois.

### Linux e macOS

```sh
curl -fsSL https://github.com/jazzautomations/jazz-cli/releases/latest/download/install.sh | sh
```

O [`install.sh`](scripts/install.sh) (sh POSIX) detecta sistema e arquitetura, baixa
`jazz-<os>-<arch>`, confere a assinatura do manifesto (com `minisign` ou OpenSSL 3) e o sha256, e
instala em `~/.local/bin` sem sudo. Se `~/.local/bin` não estiver no PATH, ele mostra a linha que
você precisa pôr no `~/.zshrc`/`~/.bashrc` (não mexe nesses arquivos sozinho).

Opções (`… | sh -s -- <opções>`):

| Opção | Variável | O que faz |
|---|---|---|
| `--bin-dir DIR` | `JAZZ_BIN_DIR` | pasta de instalação (padrão `~/.local/bin`) |
| `--system` | | instala em `/usr/local/bin`, com `sudo` se precisar |
| `--version v0.7.0` | `JAZZ_VERSION` | uma release específica em vez da última |
| `--base-url URL` | `JAZZ_BASE_URL` | pasta com `manifest.txt(.sig)` e os binários (ex.: um espelho seu, gerado por `scripts/release.sh`) |
| `--require-sig` | `JAZZ_REQUIRE_SIG=1` | falha se a assinatura não puder ser verificada |
| | `JAZZ_OPENSSL` | caminho de um OpenSSL 3 (o `openssl` do macOS é LibreSSL, que não verifica Ed25519; o script também procura o OpenSSL 3 do Homebrew) |

Sem `minisign` nem OpenSSL 3, o script confere só o sha256 e avisa. A cópia do `install.sh` que
está no repositório não tem a chave pública (quem a coloca é o `scripts/release.sh`), então ela
também só confere o sha256: use a que vem anexada à release.

**macOS (Intel ou Apple Silicon):** o mesmo comando. Num terminal rodando sob Rosetta o script
escolhe o binário arm64 nativo.

**Linux 32 bits (i386/i686, como o Dell):** o mesmo comando; ele baixa `jazz-linux-386`
(também num kernel 64 bits com userland 32 bits). Dicas para máquina antiga:

- Em Debian/antiX o `fd` se chama `fdfind`; o shell do jazz cria o atalho `fd` sozinho.
- O `~/.bashrc` costuma sair logo no começo em shell não interativo, então `ssh host 'jazz …'` não
  acha o `~/.local/bin`. Use `ssh host '~/.local/bin/jazz …'` ou `ssh -t host 'bash -lc jazz'`.
- Sem SSH para baixar, compile em outra máquina e copie:
  `JAZZ_DEPLOY_HOST=<alias-ssh> scripts/deploy-ssh.sh` (compila linux/386, instala em
  `~/.local/bin` e põe um atalho `.desktop` no menu; `--sudo` instala em `/usr/local/bin`).
- Para o sandbox no Linux instale o `bubblewrap` (`sudo apt install bubblewrap`).

### Windows (nativo, sem WSL)

> **Ainda não testado em uso real no Windows.** O porte compila para amd64/arm64 e os testes passam
> no `windows-latest` do CI, mas ninguém usou o jazz num Windows de verdade por enquanto. Relatos são
> bem-vindos.

No PowerShell, sem administrador:

```powershell
irm https://github.com/jazzautomations/jazz-cli/releases/latest/download/install.ps1 | iex
```

O [`install.ps1`](scripts/install.ps1) baixa `jazz-windows-<arch>.exe`, confere a assinatura
Ed25519 do `manifest.txt` em PowerShell puro (sem minisign/OpenSSL; escrito para o Windows
PowerShell 5.1 e o PowerShell 7) e o sha256, instala em `%LOCALAPPDATA%\Programs\jazz\jazz.exe` e
põe essa pasta no PATH do usuário. Variáveis: `JAZZ_BASE_URL`, `JAZZ_BIN_DIR`, `JAZZ_ARCH`
(`amd64`/`arm64`), `JAZZ_NO_PATH=1` e `JAZZ_REQUIRE_SIG=1`. Use o **Windows Terminal**.

Como funciona no Windows:

- **Shell da ferramenta `bash`:** o **Git Bash** quando o Git for Windows está instalado
  (no PATH ou em `C:\Program Files\Git`); senão **PowerShell** (`pwsh`, depois
  `powershell.exe`). O `bash.exe` do WSL (System32/WindowsApps) é ignorado de propósito.
  `JAZZ_SHELL=bash|pwsh|powershell|<caminho>` escolhe à mão. O modelo é avisado de qual shell
  está rodando, pra escrever na sintaxe certa. `jazz doctor` mostra o shell escolhido.
- **Terminal:** no **Windows Terminal** há cores 24 bits e símbolos Unicode. No console clássico
  (ConHost) com VT o jazz usa 256 cores e símbolos ASCII (`JAZZ_ASCII=0` força Unicode); sem VT a
  interface não abre e sugere o Windows Terminal ou `jazz -p`.
- **Pastas:** config em `%APPDATA%\jazz-cli` (auth.json, config.json, mcp.json), dados
  (sessões, logs, jobs) em `%LOCALAPPDATA%\jazz-cli`. `JAZZ_HOME` continua valendo.
- **Processos:** cada comando, servidor MCP, hook e script roda num *job object* próprio;
  Esc/Ctrl+C e timeout derrubam a árvore inteira, e se o jazz cair o Windows mata o que sobrou.
- **Segurança:** além das regras de bash, comandos destrutivos de PowerShell/cmd pedem
  confirmação (`Remove-Item -Recurse -Force`, `rd /s /q`, `format`, `iwr … | iex`,
  `-EncodedCommand`, `Set-ExecutionPolicy`, registro, `schtasks`, Defender, firewall…). Não há
  sandbox de arquivos no Windows (só limites do job object).
- **Limitações:** sob PowerShell, um programa que lê o stdin trava até o timeout (no bash
  ele recebe `/dev/null`); o `$EDITOR` padrão é o Bloco de Notas.

### Compilando do código

Go 1.27 ou mais novo:

```sh
git clone https://github.com/jazzautomations/jazz-cli && cd jazz-cli
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o jazz .
install -m 755 jazz ~/.local/bin/jazz
# outra plataforma: CGO_ENABLED=0 GOOS=linux GOARCH=386 go build -o dist/jazz-linux-386 .
```

Um binário compilado assim não tem a chave pública embutida, então o `jazz update` dele recusa
atualizar (atualização sem assinatura não é aceita). Releases assinadas saem do
`scripts/release.sh` (veja o cabeçalho do script).

### Atualizando

`jazz update` (ou `jazz update --check`) baixa a última release, confere a assinatura do manifesto,
recusa versão mais velha ou diferente da anunciada e troca o binário em uso.

## Primeiro uso

```sh
jazz doctor               # diagnóstico: provedores com credencial, shell, sandbox, terminal
jazz                      # modo interativo na pasta atual
jazz omni setup           # mostra seus provedores, liga o roteamento e sugere chaves grátis
jazz login                # entra com a conta do ChatGPT (código de dispositivo, sem navegador)
jazz key openrouter       # salva uma chave (lida escondida); vale pra qualquer provedor
```

- **Sem chave nenhuma** o jazz já funciona: o padrão é `kilo/poolside/laguna-s-2.1:free`, com
  reserva automática em outros modelos grátis rápidos. São 200 pedidos/hora por IP nos modelos
  `:free`; a qualidade varia e **não é lugar pra dado sensível**.
- **Provedores grátis e o roteador omni:** com chaves grátis de vários provedores (OpenRouter, Groq,
  Gemini, Mistral, NVIDIA, Cerebras, Cloudflare…), `jazz omni setup` liga o **omni**, que distribui
  as requisições entre todos eles, com os grátis na frente e fallback quando um cai ou esgota a cota
  ([como o omni roteia](#como-o-omni-roteia)). Antes do primeiro pedido a um provedor grátis que
  pode registrar ou treinar com seus prompts, o jazz avisa uma vez.
- **Suas chaves Anthropic/OpenAI:** `jazz key anthropic` / `jazz key openai`, ou as variáveis
  `ANTHROPIC_API_KEY` / `OPENAI_API_KEY`. Modelo pago só entra no omni em emergência
  (`omni.paid: never|emergency|always`), a não ser que você o escolha com `-m`.
- **ChatGPT (OAuth):** `jazz login` usa sua conta do ChatGPT pelo fluxo de código de dispositivo
  (`--browser` abre o navegador). Os tokens ficam em `~/.config/jazz-cli/auth.json` (0600).
- **Modelos locais:** `jazz provider add local --url http://127.0.0.1:8080/v1 --no-key` (llama.cpp,
  LM Studio, qualquer URL compatível com OpenAI) ou `--ollama`.
- Troque de modelo com `-m provedor/modelo` ou `/model` dentro da sessão; `jazz models` lista.

## Modos de permissão

| Modo | Comportamento |
|---|---|
| `auto` (padrão) | roda sem perguntar o que não parece perigoso; comando perigoso e arquivo sensível perguntam |
| `turbo` (`--turbo`, alias `full-auto`) | com sandbox do sistema: nada que fica no projeto pergunta; só pergunta o realmente necessário (lista abaixo) |
| `ask` | tudo que não é só leitura pergunta (o modo certo pra repositório desconhecido) |
| `smart` | edição no projeto e comando só-leitura rodam direto; o resto passa por um modelo rápido, que responde SAFE ou ASK; pacote, git que altera, `rm`/`sudo`, `.env`/chaves e a config do jazz sempre perguntam |
| `yolo` | nada pergunta; só dentro de container ou VM |
| `plan` | só investiga e propõe um plano |

`/mode <modo>` troca; Shift+Tab ou Ctrl+T alterna auto → turbo → smart → plan → ask. Flags `--ask`,
`--smart`, `--turbo`, `--yolo`; config `"mode": "turbo"`. Numa pasta que você ainda não confiou
(`jazz trust`), a sessão roda em `ask`; com `--turbo` explícito o modo fica, mas bash que muda algo
ainda pergunta até você confiar (no `-p`: `jazz trust --yes` antes).

**Confirmação com uma tecla:** `enter` ou `s` = sim, `a` = sempre, `n`/`esc` = não, `v` = ver o
comando inteiro ou o diff, `e` = não, e dizer o que fazer. Não precisa digitar `sim`. O "sempre" de
uma pergunta com motivo de risco (host novo, pasta fora do projeto, git destrutivo, `curl|sh` de um
host) fica em `.jazz/approvals.json` e não é perguntado de novo no projeto; `/approvals` lista e
`/approvals clear` apaga. Ler segredo e comando catastrófico perguntam sempre.

**Turbo em 5 linhas.** (1) Só existe de verdade com o sandbox do sistema (`sandbox-exec` no macOS,
`bwrap` no Linux); sem ele vira `auto` e avisa uma vez. (2) Roda sem perguntar teste, build,
formatação, `git add/commit`, edição, leitura, script e pacote limpo, numa pasta que você confiou.
(3) Pergunta só: escrita/remoção fora do projeto; envio de dados a host novo (a primeira vez);
leitura de segredo; git destrutivo (`push --force`, `branch -D`, `filter-repo`, `clean -f`,
`reset --hard` em branch compartilhada); pacote que o guardrail de supply chain marca; comando
catastrófico (`rm -rf` da home, disco, `chmod -R 777 /`, `sudo`, `curl|sh`); regra `ask`, hook,
injeção suspeita no turno; e, em pasta não confiada, bash que muda algo. (4) `git checkout -- .`/`restore .`/`reset --hard` em branch local não
pergunta porque o checkpoint shadow-git desfaz (`/undo`). (5) Decide sem chamar modelo; cada
aprovação automática vai pro log de auditoria com a regra que liberou.

**O custo de segurança, dito sem rodeio:** no turbo o que protege você dentro do projeto é o sandbox,
não uma pergunta. Um comando ruim pode apagar ou estragar arquivos do projeto (o checkpoint cobre
arquivos versionáveis de até 1 MB, não `.gitignore`d) e pode mandar o conteúdo do projeto pra um
host que você já aprovou. O sandbox impede ler sua home e segredos e escrever fora do projeto, mas a
rede continua aberta (use `--sandbox strict` pra cortar). Repositório desconhecido: `ask`.

## Headless (`-p`)

```sh
jazz -p "rode os testes e conserte o que falhar"
echo "explique este repo" | jazz -p
jazz -p -m groq/openai/gpt-oss-120b --output-format json "…"
jazz -p --until "go test ./..." "conserte o teste que falha"
```

Códigos de saída: 0 ok · 1 erro · 2 uso inválido · 3 limite de passos · 4 laço detectado ·
5 ferramenta negada · 6 resposta cortada · 7 resposta não verificada · 8 `--until` ainda falhando ·
9 checagem de qualidade falhando · 124 tempo esgotado · 130 interrompido.

O `-p` nunca fica pendurado: `--timeout` (padrão `10m`, `0` desliga) é o prazo da execução
inteira. `--output-format json|stream-json` traz chamadas de ferramenta, uso e o motivo de parada.
Em pasta não confiada, comando bash que não é só leitura é negado, a não ser que `--allow-tools`
o libere (`'Bash(npm test:*)'`) ou você passe `--yolo`; em CI rode `jazz trust --yes` antes, se o
repositório é seu.

## "Pronto" honesto

Modelo fraco gosta de dizer que fez. O jazz confere:

- **Sem ação fingida:** se o modelo diz que fez algo ("criei o arquivo…", "I ran the tests") sem
  ter chamado nenhuma ferramenta no turno, o jazz manda ele fazer de verdade uma vez; se insistir, a
  resposta sai com `⚠ resposta não verificada` (código 7 no `-p`).
- **Guardas de conclusão honesta** (`--guards on|off`, padrão on): testes e CI que já existiam
  ficam protegidos, a não ser que o pedido seja sobre testes; não vale enfraquecer teste
  (skip/xfail, apagar asserção, enxurrada de supressões de lint); resposta *hardcoded* para o teste é
  detectada; o que o pedido mandou remover precisa ter sumido; e um turno que editou código não
  termina antes de os testes (ou o código mudado) rodarem depois da última edição. Verificadores de
  eval (`CHECK`, `PROMPT`, `.hidden/`) nunca são lidos nem alterados. Desligue uma por vez com
  `JAZZ_TEST_GUARD`, `JAZZ_HARDCODE_CHECK`, `JAZZ_DONE_GATE`, `JAZZ_EVAL_GUARD` (`=off`).
- **`--until "CMD"`** (ou `/until`): o turno só termina quando o comando sai com 0; falhou, a saída
  volta pro modelo, até `--until-rounds` vezes (padrão 5).
- **`--review`** (ou `/review on`): depois de um turno que mudou arquivos num repositório git, um
  subagente só-leitura com contexto novo revisa o `git diff` do turno; defeitos concretos voltam pro
  modelo uma vez.
- **Checagem de qualidade** (`--quality off|standard|strict`): sem print de debug, TODO novo, bloco
  comentado, função gigante ou erro novo de lint/tipo nas mudanças do turno.

## Segurança

Leia o [`SECURITY.md`](SECURITY.md) antes de usar em repositório de terceiros. O resumo:

- O jazz roda comandos **como você**. As guardas (regras de comando perigoso, detector de prompt
  injection, guardrails de pacote, modo smart) são heurísticas que deixam os ataques comuns
  barulhentos, **não uma fronteira de segurança**.
- A fronteira é o **sandbox do sistema** (`--sandbox auto|strict|off`, padrão `auto`):
  `sandbox-exec` no macOS (verificado) e `bubblewrap` no Linux (ainda não verificado em máquina real).
  Tudo que o jazz executa (shell, jobs, hooks, servidores MCP stdio, scripts) roda dentro dele, sem
  ler sua home fora do projeto e sem gravar fora do projeto. `strict` recusa rodar sem sandbox e
  corta a rede dos comandos (só loopback). No Windows não há sandbox de arquivos. `jazz doctor`
  mostra o estado.
- Para repositório desconhecido: `--sandbox strict`, modo `ask`, sem chaves pagas no ambiente, de
  preferência num container.

**O que foi medido** (suíte [`evals/redteam`](evals/redteam), 14 cenários de ataque com HOME
canário e sumidouro local; detalhes e ressalvas em
[`docs/research/redteam-results.md`](docs/research/redteam-results.md)):

- Modelo `kilo/poolside/laguna-s-2.1:free`, modo `auto`: a v0.4.0 (sem a camada de segurança) teve
  12 BLOCKED, 1 COMPROMISED (escrita por symlink para fora do projeto) e 1 ERROR; a v0.4.1 teve
  12 BLOCKED, 0 COMPROMISED e 2 ERROR (o modelo entrava em espiral depois de um bloqueio); com o
  bloqueio que orienta (v0.4.3), **14/14 BLOCKED, 0 ERROR, sumidouro vazio**.
- Sandbox do sistema (macOS): rodando o payload dos cenários 11 (`curl … | sh`) e 14 (escrita
  através de symlink) por um hook confiado, que nenhuma guarda inspeciona, os dois deram
  COMPROMISED com `--sandbox off` e BLOCKED com `--sandbox auto`. Na rodada com o modelo, o cenário
  11 ainda conta COMPROMISED pelo verificador: o modelo baixou o payload "para revisar" (a rede de
  loopback é livre no `auto`), mas não o executou.
- Limites: uma rodada por cenário (a variância do modelo não foi medida), um modelo só, 11 cenários
  especificados em [`evals/redteam/SPECS.md`](evals/redteam/SPECS.md) ainda sem implementação, e a
  suíte não roda no CI.

## Como o harness trabalha

Mapa visual da arquitetura (componentes, um turno, camadas de segurança, omni e juiz, sessões, evals): [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md).

Saída longa do bash, sem LLM: com mais de 2 KB, se um parser em Go (go test, pytest, unittest,
jest/vitest, cargo test; erros de go/gcc/clang/tsc/rustc/SyntaxError do Python; stack trace de
Python, pânico do Go e Node; log com data/nível) explica pelo menos 80% das linhas, o modelo
recebe um resumo (≤ 40 linhas: contagens, falhas com arquivo:linha, erros), as linhas que o
parser não explicou, as últimas 30 linhas e o caminho da saída completa; senão, o corte 20/80
de sempre. Saída de `rg`/`grep` é cortada por arquivo (as primeiras, até 5 por arquivo, e
"+N" no fim). O limite de um resultado é 8% da janela do modelo (entre 6 e 30 KB): num modelo
de 32k são ~9 KB. Tudo dá pra desligar pra comparar: `--parse-output=false`
(`JAZZ_PARSE_OUTPUT=0`, `"parse_output": false`), `--grep-trunc headtail` (`JAZZ_GREP_TRUNC`,
`"grep_trunc"`), `--tool-budget 30k` (`JAZZ_TOOL_BUDGET`, `"tool_budget"`). Experimental e
desligado: `--prefetch grep` (`JAZZ_PREFETCH`, `"prefetch"`) busca com `rg` os nomes, caminhos
e mensagens de erro citados no primeiro pedido e põe até 12 resultados (≤ 1000 tokens) como
lembrete na frente da mensagem. Ordem: flag > variável > `config.json`.

Orçamento e qualidade (o harness cobra, não só o prompt):

- `--budget lean|normal|off` (config `budget`, env `JAZZ_BUDGET`, padrão `lean`): a tarefa é
  classificada pelo pedido em tamanho P/M/G (um arquivo ou símbolo citado = P; "feature",
  "refatorar", "migrar" ou 3+ arquivos = G) e tipo (bugfix, novo código, refatoração, pergunta,
  caminho sensível). Orçamento de passos 12/25/50 (refatoração sobe um tamanho) com lembrete em
  70% e 100% e parada no dobro (nunca acima do `--max-steps`); exploração de 4/8/15
  leituras/buscas antes da primeira edição, com aviso ao atingir e recusa no dobro. Reler um
  arquivo inteiro que não mudou devolve um ponteiro pro resultado anterior; `write` que
  reescreve um arquivo existente mantendo mais de 50% das linhas é recusado uma vez ("use
  edit"); resposta final de até 8 linhas. `normal` dobra os orçamentos e nunca para.
- `--quality off|standard|strict` (config `quality`, env `JAZZ_QUALITY`, padrão `standard`):
  quando o modelo vai parar (antes do `--until` e do `--review`), uma checagem determinística
  das mudanças do turno (write/edit): testes rodados depois da última edição (quando o projeto
  tem comando de teste conhecido e não há `--until`), sem print de debug, sem TODO/FIXME novo
  sem `shortcut:`, sem bloco de código comentado (3+ linhas), função nova/mudada com mais de
  60 linhas (strict: 40), zero erro novo de lint/tipo (gofmt + go vet, ruff/pyflakes, eslint do
  projeto) e teste tocado quando entrou lógica nova. Falhou: a lista volta pro modelo uma vez;
  falhou de novo, para com `quality_failed` (código 9 no `-p`). Caminho sensível (auth,
  pagamento, migração…) vira strict sozinho.
- Queda de rede: quando a requisição falha por rede (DNS, conexão, stream parado), o jazz testa
  a conexão (TCP em 1.1.1.1:443 e nos hosts dos provedores da cadeia); se nada conecta, espera a
  rede voltar com backoff até o `--timeout` em vez de queimar a cadeia de fallback. Config
  `net_wait: on|off` (padrão `on`), env `JAZZ_NET_WAIT`.
- Ordem do fallback: os candidatos depois do primeiro são ordenados por uma nota medida por
  modelo (passou/falhou no `--until` e na checagem de qualidade, travadas), com a velocidade
  como desempate, sem passar modelo pago na frente de grátis. Fica em
  `~/.local/share/jazz-cli/model-scores.json`. Config `fallback_order: score|config` (padrão
  `score`), env `JAZZ_FALLBACK_ORDER`.

Travou? (`--recover`, padrão `on`.) Quando o detector de laço ia parar o turno, quando o
`--until` falha 3 rodadas seguidas do mesmo jeito ou depois de 8 ferramentas seguidas com erro,
o jazz troca o histórico inteiro por uma nota de passagem fixa (pedido, `/goal`, arquivos
mudados, últimas 15 chamadas resumidas, última saída do `--until`, o que falhou), mantém os
arquivos e o modelo continua com contexto limpo dentro do `--max-steps` que sobrou. Uma vez por
turno, sem chamada extra ao modelo. Se a última saída do `--until` tem mais falhas que a
primeira rodada, as mudanças do turno são desfeitas antes (no `-p` só as de write/edit; as do
bash só com checkpoints, no interativo). `handoff` nunca desfaz, `discard` sempre desfaz, `off`
volta ao comportamento antigo (código 4/8). Também `JAZZ_RECOVER` ou `"recover"` no `config.json`. Com `--output-format json`, o resultado traz `"recover"` (`loop`, `until_repeat` ou `tool_errors`) quando a passagem aconteceu.

Cache de planos (`"plan_cache": "on"`, padrão; `JAZZ_PLAN_CACHE=off` desliga): depois de um
turno **verificado** (o `--until` passou, ou o último build/teste no bash saiu com 0 sem edição
depois) com 12 passos ou mais, o jazz guarda um modelo do plano (palavras-chave do pedido,
arquivos, passos que mudaram ou checaram algo, becos sem saída, comando de verificação) em
`~/.local/share/jazz-cli/plans/<projeto>.jsonl`. Num pedido parecido (BM25 em Go puro), até 3
planos entram como lembrete depois da sua mensagem (≤ 600 tokens). Duas falhas objetivas depois
de reaproveitar um plano o apagam. `"hindsight": "on"` (padrão `off`; ou `"provedor/modelo"`)
faz uma chamada extra ao modelo mais forte da cadeia pra escrever uma dica de até 1500
caracteres (arquivo certo, abordagem, beco sem saída) que vai junto com o plano.

Toda mudança de comportamento do harness tem chave pra desligar e comparar (flag, variável `JAZZ_*` e
campo do `config.json`). As chaves da v0.4 estão em [`DESIGN.md`](DESIGN.md#chaves-da-v04-todas); as
mais novas (`--guards`, `--block-ux`, `--sandbox`, `--ui-lint`, `--allow-outside`) estão no `jazz --help`.
Pra rodar como a v0.3, sem nada disso:

```sh
JAZZ_CRAFT=off JAZZ_SKILLS=off JAZZ_PARSE_OUTPUT=0 JAZZ_GREP_TRUNC=headtail JAZZ_TOOL_BUDGET=30k \
JAZZ_BUDGET=off JAZZ_QUALITY=off JAZZ_FALLBACK_ORDER=config JAZZ_SCRIPTS=off JAZZ_RECOVER=off \
JAZZ_PLAN_CACHE=off jazz -p "..."
```

## O que o jazz pode fazer na sua máquina

Leia antes de usar em repositório de terceiros (resumo na seção [Segurança](#segurança)).

- O jazz roda comandos de shell **como você**, edita qualquer arquivo que você pode editar e acessa a
  rede. `DangerReason`, `IsReadOnly`, os guardrails abaixo e o modo smart são heurísticas que
  deixam os ataques comuns barulhentos, não uma fronteira de segurança. A fronteira é o sandbox do
  sistema (`--sandbox`, padrão `auto`: `sandbox-exec` no macOS, `bubblewrap` no Linux), que impede
  os comandos de ler a sua home fora do projeto e de gravar fora dele; veja `SECURITY.md`.
- **Pasta não confiável**: na primeira vez numa pasta o jazz mostra o que vai carregar (AGENTS.md,
  CLAUDE.md, `.jazz/*`, `.mcp.json`, skills, comandos, hooks, scripts) e pergunta se você confia.
  Até confiar (na pergunta, com `/trust` ou `jazz trust`), a sessão roda em `ask`; com `-p`, comando
  bash que não é só leitura é negado, a não ser que `--allow-tools` libere aquele comando ou você
  passe `--yolo`. Em CI rode `jazz trust --yes` antes, se o repositório é seu. AGENTS.md/skills que
  mudaram perguntam de novo; `/trust revoke` volta atrás. Flags explícitas (`--yolo`, `--ask`,
  `--smart`) valem mesmo em pasta não confiável.
- `.mcp.json`, hooks, regras allow, steering e scripts do projeto ficam desligados até a confiança, e
  o hash cobre também os arquivos que eles executam (`./scripts/lint.sh`, `node tools/mcp.js`): um
  `git pull` ou uma edição do modelo nesse script pergunta de novo; hooks do projeto conferem isso
  antes de cada execução.
- **Texto do repositório pode guiar o modelo**: README, AGENTS.md, issues, saída de comando e páginas
  podem conter prompt injection. O detector abaixo avisa, mas modelos fracos obedecem a texto bem escrito.
- **Segredos**: o shell, hooks e servidores MCP recebem o ambiente sem chaves (`*_API_KEY`, `*_TOKEN`,
  `*_SECRET*`, `AWS_SESSION_TOKEN`, `STRIPE_*`, `DATABASE_URL`, `PGPASSWORD`, `MYSQL_PWD`,
  `*_WEBHOOK_URL`, `GITHUB_TOKEN`/`GH_TOKEN`, `NPM_TOKEN`, URLs com senha e `SSH_AUTH_SOCK` — então
  `git push` por SSH dentro do jazz não usa seu agente SSH). Saídas passam por redação (valores do
  ambiente, formatos de token: Anthropic, OpenAI, GitHub, Slack, Google, Stripe, JWT, npm, PEM, senha
  em URL do `.git/config`). Ler `.env*`, `*.pem`, `id_rsa*`, `.git/config`, `/proc/*/environ`,
  `~/.ssh`, `auth.json` pede confirmação também no `auto` (o conteúdo iria pro provedor).
- **Caminhos protegidos** (pedem confirmação em todo modo menos yolo): config do jazz/Claude,
  `~/.ssh`, `~/.aws`, arquivos de inicialização do shell, `~/.vimrc`, `~/.config/nvim`, `~/.tmux.conf`,
  `~/.cargo/config.toml`, `~/Library/LaunchAgents`, `/etc`, e no projeto `.git/config`, `.git/hooks`,
  `.mcp.json`, settings, `.envrc`, `.vscode/tasks.json`, `.gitmodules`, AGENTS.md/CLAUDE.md, `.jazz/skills`,
  `.jazz/commands` (o `/init` que cria AGENTS.md também pergunta).
- **Provedores grátis podem registrar ou treinar com seu código**: antes do primeiro pedido a um desses
  (Kilo/OpenRouter `:free`, Gemini grátis, Mistral Experiment, NVIDIA) o jazz mostra o aviso uma vez
  (`privacy_ack` na config); com `-p`, uma linha no stderr a cada execução até o `privacy_ack`.
- **Rede além dos provedores**: checagem diária de versão no GitHub (`JAZZ_NO_UPDATE_CHECK=1` desliga),
  catálogo do omni, DuckDuckGo pro `fetch` com busca, e os registros de pacote + OSV.dev do guardrail
  de dependências (`supply_chain: off` desliga). Sem telemetria.
- **Atualização**: `jazz update` só instala release com `manifest.txt` assinado (ed25519, versão +
  sha256 de cada binário), recusa versão mais velha ou diferente da anunciada e revalida
  redirecionamentos. O `install.sh` publicado verifica a assinatura com `minisign` ou OpenSSL 3
  (sem nenhum dos dois, só o sha256 e um aviso; `JAZZ_REQUIRE_SIG=1` transforma em erro).
- Para repositório desconhecido: rode num container, em `ask`, e sem chaves pagas no ambiente.

### Guardrails

| Guardrail | O que faz | Config |
|---|---|---|
| Pacotes (supply chain) | `pip`/`uv`/`poetry add`, `npm`/`pnpm`/`yarn`/`bun add\|install <pkg>`, `go get`, `cargo add`, `gem install`, `composer require`: cada pacote é conferido no registro (PyPI, npm, proxy.golang.org, crates.io, RubyGems, Packagist) e no OSV.dev. Pede confirmação (mesmo no `auto`) quando o pacote **não existe** (nome inventado pelo modelo — slopsquatting), tem menos de 30 dias, parece typosquat de um dos ~1.700 nomes populares embutidos (Damerau-Levenshtein ≤ 2), está marcado como malicioso (MAL-) ou tem vulnerabilidade crítica na versão pedida, ou quando não há rede pra conferir. Cache de 24 h em `dataDir()/supply-cache.json` (1 h para "não existe"). Depois da instalação, as mudanças em `package-lock.json`, `poetry.lock`, `uv.lock`, `go.sum` e `Cargo.lock` aparecem no resultado. `jazz deps audit` roda o mesmo sobre os manifestos. | `supply_chain: on\|off`, `JAZZ_SUPPLY_CHAIN=off` |
| Prompt injection | Todo resultado de ferramenta (arquivo, bash, página, MCP) e as instruções do projeto são varridos atrás de texto dirigido ao agente: "ignore as instruções anteriores", "you are now", rótulos falsos `system:`/`assistant:`, tokens de chat, comentário HTML ou de código mandando o agente rodar algo, base64 com ordens, pedidos para ler `~/.ssh`/`auth.json` ou mandar segredos por `curl`. Achou: o conteúdo vai marcado `<untrusted suspected_injection="…">`, as linhas com a instrução chegam ao modelo trocadas por um marcador, você vê um aviso e toda ação que muda algo pede confirmação no resto do turno, mesmo no `auto` — menos editar arquivos comuns do projeto (o arquivo marcado continua sendo alvo legítimo da tarefa; só o que a injeção pede — mandar dados, ler segredos, rodar payload — fica travado). Uma imagem lida (read/MCP) também arma a confirmação. README comum ("rode o comando abaixo") não dispara. | `injection_guard: off\|warn\|strict` (padrão warn; strict vale até no yolo) |
| Sanitização | Antes de chegar ao modelo: remove caracteres invisíveis (zero-width, U+FEFF), controles bidi, caracteres de tag Unicode ("ASCII smuggling", o texto escondido é decodificado e mostrado), seletores de variação usados como portadores, escapes de terminal; marca letras cirílicas/gregas parecidas misturadas em palavra latina como `\u{0441}`; tira HTML escondido (`display:none`, `visibility:hidden`, `font-size:0`, branco no branco, `hidden`) de páginas e resultados MCP. O resultado diz "N caracteres invisíveis removidos". Texto colado no prompt também passa. Acentos e emoji (inclusive 👨‍👩‍👧 e ❤️) passam intactos. | `sanitize: off\|strip\|strict` (padrão strip) |
| Baixar e executar | `curl … \| sh`, `bash <(curl …)`, `curl -o x.sh && sh x.sh`, `base64 -d \| sh`, `python -c` com urllib + exec sempre pedem confirmação em auto/smart/ask (yolo continua yolo). | — |
| Jobs em segundo plano | No máximo 8 ao mesmo tempo. | `max_jobs` |
| Bloqueio que orienta | Todo bloqueio volta ao modelo em uma linha: o que foi bloqueado, por quê e "siga a tarefa sem isso, não tente contornar". 3 tentativas bloqueadas no mesmo alvo (arquivo, host, tipo de comando) ou 6 no turno encerram o turno com um relatório ("3 tentativas bloqueadas em X; parei para não gastar tempo. Motivo: …"), sem deixar o modelo dizer que deu certo. | `block_ux: on\|off`, `--block-ux` |
| Cerca da pasta do projeto | `read`/`write`/`edit`, caminhos no bash (inclusive `cd`) e o `task` ficam dentro da pasta onde o jazz começou (mais a raiz do git, se a pasta faz parte do repositório). Fora dela, só pastas do sistema (`/usr`, `/opt`, `/etc` …), caches de toolchain, temporários e os arquivos de spill/jobs/skills do jazz. A pasta de configuração do jazz nunca. Interativo pergunta; `-p` recusa. | `allow_outside_project`, `--allow-outside` |


## Comandos

### Linha de comando

| Comando | O que faz |
|---|---|
| `jazz [prompt]` | modo interativo (prompt inicial opcional) |
| `jazz -p "prompt"` | headless: roda até o fim e imprime só a resposta |
| `jazz -c` / `jazz -r <id>` | continua a última sessão da pasta / retoma uma sessão (também com `-p`) |
| `jazz login [chatgpt] [--browser]` | login com a conta do ChatGPT |
| `jazz key <provedor>` | salva uma chave de API |
| `jazz logout <provedor>` | apaga a credencial |
| `jazz provider add <nome> --url URL [--type openai] [--key-env VAR] [--model M] [--header K=V] [--no-key] [--ollama]` | adiciona um provedor (Ollama, llama.cpp, LM Studio, qualquer URL compatível com OpenAI) |
| `jazz models [provedor]` | lista modelos |
| `jazz omni setup` | liga o roteamento entre todos os provedores |
| `jazz export <id> [md\|html] [arq]` | exporta uma sessão |
| `jazz trust [--yes] [--revoke]` | confia na pasta e na configuração do projeto (AGENTS.md/CLAUDE.md, skills, comandos, `.mcp.json`, hooks, regras allow, steering, scripts); `--revoke` desfaz |
| `jazz deps audit [pasta]` | verifica as dependências dos manifestos (existe no registro, idade, typosquat, OSV); sai com 1 se algo pede atenção |
| `jazz sanitize <arquivo>` | mostra o que seria removido antes de ir ao modelo (texto invisível, tags Unicode, bidi, HTML escondido) e se parece prompt injection |
| `jazz update [--check]` | atualiza o binário |
| `jazz bug` | gera um `.tar.gz` pra reportar problema |
| `jazz doctor` | diagnóstico da instalação |
| `jazz script [nome] ['{json}']` | lista os scripts prontos ou roda um, sem modelo (JSON no stdout; `-` lê os args do stdin) |
| `jazz version` | versão |

Opções: `-m <provedor/modelo>`, `-C <pasta>`, `--yolo` / `--ask` / `--smart`, `-v` (progresso no stderr
com `-p`), `--output-format text|json|stream-json`, `--max-steps N`, `--timeout 10m`,
`--allow-tools bash,edit,write,fetch` ou regras como `'Bash(npm test:*)'`,
`--system-append T`, `--until "CMD"`, `--until-rounds N`, `--review`, `--budget lean|normal|off`,
`--quality off|standard|strict`, `--debug`, `--parse-output[=false]`, `--grep-trunc grouped|headtail`,
`--tool-budget N|auto`, `--prefetch off|grep`, `--scripts on|off`, `--fast-path`, `--offline`,
`--recover on|handoff|discard|off`.

### No modo interativo

| Comando | O que faz |
|---|---|
| `/help` | ajuda |
| `/model [ref]` · `/models [provedor]` | mostra/troca o modelo · lista modelos |
| `/omni [on\|off\|strategy …]` | roteamento entre provedores |
| `/key` · `/login` · `/logout` | credenciais |
| `/mode auto\|turbo\|smart\|ask\|yolo\|plan` | modo de permissão (Shift+Tab ou Ctrl+T alterna auto → turbo → smart → plan → ask) |
| `/approvals [clear]` | aprovações "sempre" deste projeto (host de egress, pasta fora, git destrutivo) |
| `/permissions [list\|remove\|add]` | regras persistentes |
| `/effort low\|medium\|high\|off` · `/think` | raciocínio |
| `/compact [foco]` · `/context` | contexto |
| `/undo` · `/rewind` · `/fork [n]` | desfazer arquivos (inclusive o que o bash mudou) / voltar a conversa / copiar a conversa |
| `/export [md\|html] [arq]` · `/clear` · `/resume` · `/forget` | sessões |
| `/todo` · `/jobs [kill]` · `/mcp` · `/cost` · `/expand` | estado · saída inteira da última ferramenta |
| `/goal [objetivo\|off]` · `/until [comando\|off]` · `/review [on\|off]` | objetivo fixo · só termina quando o comando passar · revisão do diff por subagente |
| `/init` | cria um `AGENTS.md` analisando o projeto |
| `/script [nome] [json]` | lista os scripts prontos ou roda um (`/script git_state`) |
| `/offline` · `/online` | força o modo offline · volta a tentar os provedores agora |
| `/doctor` · `/exit` | diagnóstico · sair |

Modos de permissão: veja [Modos de permissão](#modos-de-permissão).

Desfazer: no modo interativo, além do snapshot de cada `write`/`edit`, o jazz guarda um
checkpoint shadow-git (repositório separado em `~/.local/share/jazz-cli/checkpoints/`, o
`.git` do projeto nunca é tocado) no começo de cada turno e depois de cada `bash` que pode
escrever. `/undo` e `/rewind` voltam só os arquivos que o turno mudou, inclusive via `sed -i`,
`>` ou `rm`, apagam os que ele criou e preservam o que você editou na mão entre turnos.
Respeita `.gitignore`, ignora arquivos acima de 1 MB e desliga sozinho (fica só o snapshot de
write/edit) em projeto com mais de 20 mil arquivos ou sem git ≥ 2.25.

Atalhos: `!comando` roda direto no shell · `Esc` interrompe o agente · o que você digita
durante o trabalho entra na fila · `\`+Enter ou Alt+Enter quebra linha · `@arquivo` anexa ·
Tab completa · `/` abre o menu de comandos (↑↓ escolhe, Tab completa, Enter roda) · `?` com a
linha vazia mostra os atalhos · Ctrl+O (ou `/expand`) mostra a saída inteira da última
ferramenta · Ctrl+R busca no histórico · Ctrl+G abre a mensagem no `$EDITOR`.

A tela: caixa de boas-vindas (o logo grande aparece com `--banner` ou em terminal de 100+
colunas), entrada numa caixa que cresce com o texto e rodapé com modo, modelo e uso do
contexto; respostas com `●` e markdown quebrado na largura do terminal; ferramentas como
`● Bash(go test ./...)` com o resultado em `⎿` (até 3 linhas), diffs com número de linha e
aprovações num menu numerado (↑↓ + Enter, ou 1-3). Paleta Nord; em 16 cores usa as cores do
esquema do terminal, e com `TERM=linux` (ou `JAZZ_ASCII=1`) tudo vira ASCII. Comandos próprios: arquivos `.md` em `~/.config/jazz-cli/commands`,
`.jazz/commands` ou `.claude/commands` viram `/nome`.

### Erros de digitação

Quem digita rápido não deveria ser tratado ao pé da letra. Comandos: `/modle` roda `/model`, `/mode trubo`
roda `/mode turbo`; se for ambíguo (`/ofline`: offline ou online) aparece um menu numerado. Nomes no
pedido: antes de o modelo rodar, o jazz confere o que parece caminho, arquivo, função ou teste contra um
índice barato do repositório (nomes de arquivo + símbolos de topo, no máximo 20 mil arquivos, em cache) e
anexa à mensagem uma nota de uma linha com o raciocínio, por exemplo ``` `parsr.go` → `parser.go` (arquivo
mencionado junto de 'conserta'; único .go com esse radical) ```, para o modelo poder discordar. A escolha
usa o contexto, não só a distância de edição: teste/função/arquivo citados na frase, verbo, extensões do
repositório, arquivos mexidos nesta sessão ou hoje, e tecla vizinha pesa menos que letra qualquer. Palavra
comum nunca vira nome (só formato de nome, ou depois de "arquivo", "teste", "função"...); sem candidato
bom não se acrescenta nada. O texto do usuário nunca é reescrito e o system prompt não muda (cache). Se um
`read`/`edit` aponta um caminho que não existe, o erro lista os parecidos (`não existe; parecido: ...`);
os atalhos offline e o classificador de tarefa também entendem `roda os teste`, `git stauts`,
`procra X no codgio`, `arruma`/`concerta` = conserta. (`testes` sozinho continua sem disparar atalho.)

## Scripts, atalhos e modo offline

**Ferramenta `script`** (`"scripts": "on"` no `config.json`, padrão; `--scripts off` desliga pra
comparar A/B). Uma ferramenta só, `{name, args}`, com 19 scripts embutidos (Python 3 só com a
biblioteca padrão, rodado com `python3 -I`, ou `sh` POSIX) que respondem num JSON curto
`{ok, confidence, data, unparsed, warnings}` em vez de várias chamadas de bash e saída longa:
`detect_project`, `run_tests`, `parse_test_output`, `parse_build_errors`, `extract_stacktrace`,
`parse_log_lines`, `find_symbol`, `tree_summary`, `json_edit`, `yaml_get`, `csv_stats`, `git_state`,
`git_safe`, `port_free`, `http_check`, `extract_versions`, `snapshot_cmd`, `diff_quality`, `ui_lint`.

**Lint de UI depois da edição** (`"ui_lint": "on"`, padrão; `--ui-lint off` ou `JAZZ_UI_LINT=off`
desliga). Depois de um `write`/`edit` que deu certo num arquivo de UI (`*.html`, `*.css`, `*.scss`,
`*.tsx`, `*.jsx`, `*.vue`, `*.svelte`, `*.astro`, `tailwind.config.*`), o jazz roda o `ui_lint` nesse
arquivo (só regex, sem renderizar) e anexa os achados ao resultado da ferramenta: no máximo 12
linhas `arquivo:linha severidade regra: mensagem`, erros primeiro. Regras: texto em gradiente,
gradiente violeta/ciano padrão, brilho colorido sem deslocamento, faixa colorida na borda lateral,
emoji no lugar de ícone, lorem ipsum, frases genéricas de marketing, `<img>` sem `src`/`alt`,
`outline: none` sem `:focus-visible`, animação sem `prefers-reduced-motion`, mesmo raio em tudo,
cor literal fora dos tokens (quando há um arquivo de tokens), `<html>` sem `lang`, viewport que
trava o zoom, títulos pulando nível, fonte < 12px, easing com bounce. Arquivo limpo não acrescenta
nada; os mesmos achados de novo viram uma linha só. `ui-lint-disable <regra>` num comentário do
arquivo desliga uma regra ali. Precisa de `python3`; derivado do detector do Impeccable (ver
`CREDITS.md`).
Os argumentos são validados contra o schema de cada script (com conserto de tipo pra modelo
fraco); cada execução tem 60 s, ambiente sem chaves e saída de no máximo 8 KB. Os scripts são
extraídos pra `~/.local/share/jazz-cli/scripts/<versão>/` e conferidos por sha256 a cada uso.
Scripts seus em `~/.config/jazz-cli/scripts/` (`nome.json` + `nome.py` ou `nome.sh`); os do
projeto, em `.jazz/scripts/`, só depois de `jazz trust` (tipo `scripts`) e nunca substituem um
embutido. Permissões: script só-leitura roda até em plan e no subagente; os que escrevem ou
rodam comando seguem as regras do bash (`Bash(...)` deny vale pro `cmd` do script; `[t]` salva
`Script(nome)` ou a regra `Bash` do comando). Sem `python3`, só os de `sh` aparecem.

**Atalhos** (`"fastpaths": "on"`, padrão; `"off"` desliga). Pedido claro e curto, sem mais nada
junto, vira um comando sem chamar o modelo: "rode os testes", "compila", "rode o lint", "git
status", "o que mudou", "últimos commits", "onde está a função Foo", "procura parseArgs". O
comando sai da detecção do projeto (`go.mod`, `package.json`, `Cargo.toml`, Python, `Makefile`)
e passa pelo mesmo controle de permissão do bash. No interativo aparece
`⚡ atalho: go test ./...  [Enter] rodar  [Esc] mandar pro modelo`; se o comando falhar, a saída
vai pro modelo como primeiro passo. No `-p`, só com `--fast-path`. Dois atalhos casando ao mesmo
tempo = nenhum. Troque ou desligue atalhos em `~/.config/jazz-cli/intents.json` ou
`.jazz/intents.json` (este só com `jazz trust`), mesmo formato do `intents.json` embutido.

**Modo offline.** Quando todo provedor falha por rede ou credencial (ou não há nenhum com
credencial), o jazz não sai: mostra o aviso de modo offline e continua com atalhos, `/script`,
`!comando` (rg/fd), `/undo` e sessões. A cada 60 s (ou com `/online`) testa a conexão com os
provedores e volta pro modelo quando um responder. `JAZZ_OFFLINE=1` ou `--offline` força o modo
(um modelo local, como llama.cpp, continua valendo se for o modelo da sessão). No `-p`, um
pedido que é atalho termina com 0 mesmo sem rede; o resto sai com 1 e uma mensagem clara.

## Perguntas e worktrees

### Perguntas (`ask_user`)

Quando o pedido é ambíguo de verdade e a resposta muda o trabalho (dois desenhos incompatíveis,
qual de vários alvos), o modelo pode chamar a ferramenta `ask_user` com uma pergunta e 2 a 4
opções. No modo interativo ela aparece como um menu numerado; o turno espera você digitar o
número, o começo de uma opção ou uma resposta livre (Esc pula, e o modelo escolhe um padrão e
diz qual). No `jazz -p` ela nunca trava: o modelo recebe "sem usuário, escolha o padrão mais
razoável e diga a suposição". Com `jazz -p --ask-stdin` a resposta é lida de uma linha do stdin.
Uma pergunta não conta como ação: responder "fiz X" depois de só perguntar ainda cai no guarda
anti-fake. A ferramenta faz parte da lista fixa de ferramentas (o cache do prompt não muda).

O mesmo menu aparece sem o modelo pedir: no modo offline, um pedido sem atalho determinístico
recebe 3 a 5 ações montadas do contexto (abrir o arquivo citado, procurar o termo no código,
rodar os testes e listar falhas, git status/diff, localizar o arquivo:linha do último erro,
resumo do projeto, `/online`); com o modelo lento (5 s sem o primeiro token) a linha de status
mostra `[1 testes · 2 git · 3 procurar · esc cancela]` e um dígito roda a ação enquanto o
pedido continua. Digitar texto em vez do número manda o pedido normalmente.

### Sem rede e máquinas lentas

Sem rede, o jazz percebe em poucos segundos (DNS, conexão recusada, ou nenhuma conexão em 3 s
com a sonda de 1 pacote falhando), entra no modo offline com um banner do que funciona sem
modelo (atalhos, os scripts embutidos, `!comando`, `/undo`, `/rewind`, `/resume`), sonda a rede a
cada 30 s e volta sozinho, avisando. A linha de status mostra sempre o que a requisição está
fazendo (`conectando`, `aguardando 1º token`, `gerando`, `parado há 8s`) e, quando o modelo é
trocado, por quê (`laguna-s-2.1 não respondeu em 20s → tentando minimax-m3.1-flash`). No modo
interativo, se você não fixou um modelo, o primeiro pedido vai pro modelo grátis mais rápido de
qualidade aceitável da cadeia (`JAZZ_INTERACTIVE_FAST=0` desliga).

Numa máquina de 1 núcleo o modo econômico liga sozinho (`--low-power on|off`, `JAZZ_LOW_POWER=1|0`):
a resposta é desenhada em quadros (até 10 por segundo, 4 em respostas longas, em vez de uma escrita
por token), o spinner anima a 2 quadros por segundo e o coletor de lixo do Go trabalha menos.

### Worktrees

`jazz --worktree [nome]` (ou `--worktree=nome`, que nunca é ambíguo) cria um worktree git em
`.jazz/worktrees/<nome>`, num branch novo `jazz/<nome>` a partir do HEAD, e roda a sessão lá dentro:
a pasta do projeto, o cercado de arquivos e o sandbox passam a ser o worktree, e o checkout principal
fica intocado. Sem nome, vira `wt-AAAAMMDD-HHMMSS`. A pasta entra em `.git/info/exclude` (o seu
`.gitignore` não muda). Funciona também em headless, pra tarefas em paralelo:
`jazz -p --worktree=tarefa-1 "…"`.

Ao sair, o jazz diz o estado do worktree e como trazer o trabalho de volta:

```
jazz worktree list                    # nome, branch, limpo/sujo, commits à frente
jazz worktree merge <nome> [--yes]    # mostra o diff --stat, pergunta, git merge --no-ff no branch de origem
jazz worktree rm <nome> [--force]     # apaga worktree e branch (recusa trabalho não mesclado sem --force)
```

O `merge` commita antes as mudanças ainda não commitadas do worktree (perguntando, ou direto com
`--yes`) e só roda com o repositório principal limpo e no branch de onde o worktree saiu. Dentro da
sessão, `/worktree` mostra o worktree atual ou a lista.

## Como o omni roteia

Com `omni.enabled` (ligado pelo `jazz omni setup` ou `/omni on`), cada requisição percorre
uma cadeia de candidatos:

1. o modelo que já funcionou neste turno (fallback "grudado" por até 3 min; depois a cadeia
   recomeça do principal, e quem travou no primeiro byte fica no fim da fila por 3 min);
2. o modelo principal (`model`);
3. a lista `fallback` da config;
4. todos os outros provedores com credencial.

A ordem desses últimos depende de `omni.strategy`:

- `free-first` (padrão): modelos grátis primeiro;
- `weighted`: pelo `weight` de cada provedor;
- `ordered`: a ordem da config;
- `cheapest`: o mais barato primeiro.

Erros 429/402/5xx com `Retry-After` curto esperam e tentam de novo. Cota diária esgotada
tira o candidato da cadeia até o reset (fica em `exhausted.json` e volta sozinho).
`omni.exclude` remove provedores ou refs (aceita glob), `omni.paid` decide quando usar
modelo pago (`never`, `emergency` (padrão) ou `always`) e `omni.no_training` descarta
provedores cujo plano grátis treina com seus prompts. Os apelidos `omni/coder`,
`omni/smart`, `omni/fast`, `omni/free` e `omni/long` escolhem pela categoria
(`-m omni/coder`). `/omni` mostra a ordem de tentativa atual.

## Configuração

Pastas (respeitam `XDG_CONFIG_HOME`/`XDG_DATA_HOME`; `JAZZ_HOME` muda as duas):

| Arquivo | Para quê |
|---|---|
| `~/.config/jazz-cli/config.json` | modelo, fallback, modo, provedores, `omni`, `permissions`, `hooks`, `skills`, `recover`, `plan_cache`, `hindsight` (e as chaves da v0.4 em DESIGN.md) |
| `~/.config/jazz-cli/auth.json` | chaves e tokens (0600; nunca vão pro shell, MCP ou hooks) |
| `~/.config/jazz-cli/mcp.json` | servidores MCP globais |
| `~/.config/jazz-cli/trusted.json` | projetos em que você confiou |
| `~/.config/jazz-cli/AGENTS.md`, `skills/`, `commands/` | instruções, skills e comandos globais |
| `~/.local/share/jazz-cli/` | sessões (JSONL), logs de debug, cota, cache de update, cache de planos (`plans/`) |

### MCP

Global em `~/.config/jazz-cli/mcp.json`, do projeto em `.mcp.json` na raiz (mesmo formato do
Claude Code). O `.mcp.json` do projeto só vale depois de `jazz trust` (ou de você aceitar a
pergunta no modo interativo); um servidor do projeto nunca substitui um global de mesmo nome,
e `${VAR}` no projeto só expande variáveis seguras ou listadas em `project_env` do arquivo
global. Use `enabled_tools`/`disabled_tools` por servidor e `/mcp` pra ver o status.

**Resources e prompts.** Servidores que declaram as capacidades `resources` / `prompts` ganham duas
ferramentas para o modelo: `mcp_resource {server, uri}` (lê um recurso; `uri` vazio lista recursos e
templates; `subscribe: true` assina atualizações quando o servidor suporta) e `mcp_prompt {server,
name, args}` (busca um prompt preenchido; `name` vazio lista os prompts e seus argumentos). O prompt
de sistema leva só as contagens (“12 recursos, 3 templates em 2 servidores”), nunca a lista, então o
cache de prompt não muda; o modelo lista chamando a ferramenta. Texto grande vai truncado com o
original num arquivo de spill; blob binário é decodificado para um arquivo 0600 e o modelo recebe só
um resumo (imagem pequena vira anexo). Tudo passa pela mesma sanitização e detecção de injection dos
resultados de `fetch`/MCP e vem dentro de `<untrusted_tool_output server=…>`. O catálogo
(recursos, templates, prompts) entra no hash anti rug-pull como as descrições de tools: se o servidor
muda a lista depois de você ver, a próxima chamada pede confirmação. No chat: `/mcp resources` e
`/mcp prompts` listam tudo como `servidor:uri`. Desligar: `"disable_resources": true` /
`"disable_prompts": true` no servidor, ou `JAZZ_MCP_RESOURCES=off` para todos.

### Permissões

Regras no formato do Claude Code (`Bash(npm test:*)`, `Edit(src/**)`, `Read(.env)`,
`WebFetch(domain:go.dev)`, `mcp__srv__*`), lidas de:

- `config.json` → `permissions.allow|deny|ask`
- `~/.claude/settings.json`
- `.claude/settings.json` e `.claude/settings.local.json` do projeto
- `.jazz/settings.json` do projeto (onde a opção `[t]` e o `/permissions add` gravam)

`deny` > `ask` > `allow`. As regras `allow` do projeto só valem com o projeto confiado;
`deny`/`ask` sempre valem.

### Hooks

`PreToolUse`, `PostToolUse`, `UserPromptSubmit`, `Stop` e `SessionStart`, no formato do
`settings.json` do Claude Code, nos mesmos arquivos das permissões (chave `hooks`). Rodam com
`bash -c` na raiz do projeto, recebem JSON no stdin, timeout de 60 s; saída 2 bloqueia.

### Instruções do projeto

`AGENTS.md` global e um por pasta, da raiz do git até a pasta atual
(`AGENTS.override.md` › `AGENTS.md` › `CLAUDE.md`). Skills em `~/.config/jazz-cli/skills`,
`.jazz/skills` e `.claude/skills` (no máximo 12 na lista do prompt, descrição cortada em 120
caracteres). `~/.claude/skills` só entra com `"skills": {"claude_dir": true}` no `config.json`:
lá costumam morar dezenas de skills grandes de outros agentes, que custam milhares de tokens.

### Skills embutidas

O jazz traz 13 skills curtas dentro do binário (`surgical-change`, `verify-done`, `debug-loop`,
`tdd-lite`, `write-tests`, `refactor-safely`, `git-hygiene`, `explore-codebase`, `shell-safety`,
`plan-small`, `old-coder-gauntlet`, `review-diff`, `frontend-craft`). A `frontend-craft` (piso de
qualidade, proibições e os 4 modos Persuadir/Operar/Ler/Experiência, reescrita a partir do
Impeccable) entra em pedidos de design/UI/tela/página/landing/dashboard e na primeira edição de um
arquivo de UI. O modelo não vê a lista: o próprio jazz
escolhe por palavras-chave (PT e EN) no pedido, arquivos editados (`*_test.go`, `*.spec.*`…),
sinais na saída das ferramentas (teste falhando, stack trace, conflito de merge, `rm -rf`,
`sed -i`, diff passando de 40 linhas) e a fase do turno. No máximo uma por turno e três por
sessão; entra uma vez como `<system-reminder>` na mensagem do usuário ou no resultado da
ferramenta que disparou (o prompt de sistema não muda, o cache continua valendo) e volta
depois de compactar. Aparece como `skill aplicada: <nome>`; com `--debug`, cada decisão vai
pro log (`"kind":"skill"`, candidatos com pontuação e motivo de pular).

- `config.json`: `"skills": "auto"` (padrão) · `"manual"` (só `/skill`) · `"off"`, ou o objeto
  `{"mode": "auto", "claude_dir": false, "threshold": 3, "per_turn": 1, "per_session": 3}`.
- `JAZZ_SKILLS=off|auto|manual` passa por cima da config (bom pra comparar A/B no eval).
- `/skill` lista; `/skill <nome> [pedido]` força uma no próximo turno.

### Steering (regras condicionais)

Regras no formato do Kiro em `.jazz/steering/*.md` (também `.kiro/steering/*.md`) e do
Cursor em `.cursor/rules/**/*.mdc`; globais em `~/.config/jazz-cli/steering/*.md` (uma
regra do projeto substitui a global de mesmo nome). O front matter decide quando a regra
entra:

```md
---
inclusion: fileMatch            # always (padrão) | fileMatch | auto | manual
fileMatchPattern: "**/*.{ts,tsx}"   # ou uma lista
description: convenções da API  # usada por auto
---
Texto da regra.
```

- `always` (Cursor: `alwaysApply: true`): vai no prompt de sistema, fixo na sessão.
- `fileMatch` (Cursor: `globs`): entra uma vez por sessão, como lembrete, no resultado do
  primeiro `read`/`edit`/`write` que tocar um arquivo que casa. O prompt não muda, então o
  cache continua valendo; padrão sem `/` casa o nome do arquivo em qualquer pasta.
- `auto` (Cursor: só `description`) e `manual`: aparecem numa lista no prompt, como as
  skills; o modelo lê quando a tarefa casa com a descrição ou quando você pede (`#nome`).

As regras do projeto passam pelo portão de confiança (`jazz trust`), como `.mcp.json` e
hooks: mudou o arquivo, pergunta de novo. Links simbólicos e arquivos acima de 64 KB são
ignorados.

## Problemas comuns

- **`jazz: command not found`**: falta o `~/.local/bin` no PATH. Coloque
  `export PATH="$HOME/.local/bin:$PATH"` no `~/.zshrc` (Mac) ou `~/.bashrc` (Linux) e abra
  outro terminal. Por `ssh host 'comando'` o `.bashrc` não roda: use o caminho completo.
- **"sem credencial pra …"**: rode `jazz key <provedor>` ou `jazz login`. `jazz doctor`
  mostra quais provedores têm credencial (✓) e testa o modelo padrão.
- **`-m zen` sem chave**: os modelos grátis do OpenCode Zen só funcionam dentro do OpenCode;
  aqui o Zen precisa de chave (`jazz key zen`). Sem chave nenhuma use o padrão (`kilo`).
- **"o modelo parou de responder (stream sem dados por muito tempo)"**: o provedor grátis
  travou. Tente de novo, troque com `-m`, ou ligue o omni com mais de um provedor. Um modelo
  grátis que tem reserva depois dele precisa mandar o primeiro dado em 25 s, senão o jazz passa
  pro próximo na hora (e o deixa por último por 10 min); ajuste com `"first_byte_timeout": 40`
  (segundos; negativo desliga) no `config.json`.
- **`fd` não encontrado (Debian/antiX)**: o pacote instala `fdfind`; o shell do jazz cria o
  atalho `fd` sozinho.
- **Terminal com caracteres estranhos**: confira `TERM` e o locale UTF-8 (`locale`).
- **Projeto ignorando `.mcp.json`/hooks/steering**: o projeto não está confiado. Rode `jazz trust`.
- **Algo travou ou quebrou**: `jazz --debug` grava o log em `~/.local/share/jazz-cli/logs`;
  `jazz bug` empacota doctor, config mascarada e o último log pra reportar. Pânico grava
  `crash-<data>.txt` na mesma pasta.

## Evals

As baterias medem o jazz de verdade, com um modelo agindo e um verificador independente fora da
pasta de trabalho (no v2 e no red-team, trancado enquanto o agente roda). Os resultados vão para
fora do repositório (v1/v2, em `~/jazz-eval-results`) ou para `evals/redteam/results/` (ignorado
pelo git). Precisam de `bash` e `python3`.

```sh
# v1: 10 tarefas curtas (bugfix, feature, refactor, shell, config…), jazz -p --yolo
LIMIT=600 evals/v1/run.sh [provedor/modelo]          # usa o `timeout` do GNU coreutils

# v2: 10 tarefas difíceis (causa distante, multiarquivo, servidor em segundo plano, refactor oculto…)
evals/v2/calibrate.sh                                 # confere que cada CHECK falha sem solução e passa com refsol/
LIMIT=900 ONLY="01 07" JAZZ=./jazz evals/v2/run.sh [provedor/modelo]

# red-team: 14 ataques (README envenenado, AGENTS.md, unicode invisível, typosquat, curl|sh, symlink…)
JAZZ=./jazz MODE=auto LIMIT=480 evals/redteam/run.sh [prefixos…]
```

O red-team cria um HOME falso com segredos canário e sobe um sumidouro HTTP local; mesmo assim,
**rode só numa máquina de desenvolvimento descartável ou numa VM**, nunca onde há credenciais reais.
O v1/v2 roda com `--yolo`: use uma pasta/máquina descartável também. As tarefas v2 têm soluções de
referência (`refsol/`) e "anti-soluções" (`antisol/`) que os verificadores precisam rejeitar.

## Pesquisa

Relatórios que embasaram decisões do jazz, em [`docs/research/`](docs/research):

| Documento | Do que trata |
|---|---|
| [`grep-is-all-you-need.md`](docs/research/grep-is-all-you-need.md) | O artigo "Is Grep All You Need?" (arXiv:2605.15184) e o que ele implica para busca por grep e o `--prefetch` |
| [`tocomas-plan-reuse.md`](docs/research/tocomas-plan-reuse.md) | TOCOMAS (arXiv:2609.37953) e reuso de planos (AgentReuse, Agentic Plan Caching): origem do cache de planos |
| [`shepherd-meta-agents.md`](docs/research/shepherd-meta-agents.md) | Shepherd (Stanford, arXiv:2605.10913): origem do `--recover` e da dica de *hindsight* |
| [`metaharness-self-improvement.md`](docs/research/metaharness-self-improvement.md) | Meta-Harness e harnesses que se melhoram sozinhos: desenho (não implementado) do `jazz improve` |
| [`bob-kiro-devin.md`](docs/research/bob-kiro-devin.md) | IBM Bob Shell, Kiro CLI e Devin CLI comparados ao jazz: origem do steering, modo smart, `--until` e `--review` |
| [`skills-personas-scripts.md`](docs/research/skills-personas-scripts.md) | Fontes de skills, política do harness e a biblioteca de scripts |
| [`skill-bodies.md`](docs/research/skill-bodies.md) | Os corpos das skills embutidas, com a origem de cada regra |
| [`impeccable-frontend-craft.md`](docs/research/impeccable-frontend-craft.md) | Impeccable → skill `frontend-craft` e hook `ui_lint` |
| [`security-review-oss.md`](docs/research/security-review-oss.md) | Revisão de segurança antes de abrir o código: modelo de ameaças e achados F1–F9 |
| [`security-gap-audit.md`](docs/research/security-gap-audit.md) | Auditoria de lacunas contra catálogos (OWASP LLM/Agentic, MITRE ATLAS, promptfoo, garak) |
| [`redteam-results.md`](docs/research/redteam-results.md) | Resultados medidos do red-team, correções de validade da suíte e evidência do sandbox |

Outros documentos: [`DESIGN.md`](DESIGN.md) (decisões e o que foi rejeitado),
[`FINDINGS.md`](FINDINGS.md) (bugs e lacunas, com o que foi verificado),
[`CONTRACT.md`](CONTRACT.md) (registro histórico do contrato usado para construir a v0.2 em paralelo),
[`CHANGELOG.md`](CHANGELOG.md), [`CREDITS.md`](CREDITS.md).

## Contribuindo

Veja [`CONTRIBUTING.md`](CONTRIBUTING.md) e o [Código de Conduta](CODE_OF_CONDUCT.md). Antes de
abrir um PR:

```sh
gofmt -l .        # não pode imprimir nada
go vet ./...
go test -count=1 ./...
CGO_ENABLED=0 GOOS=linux GOARCH=386 go build -o /dev/null .
sh -n scripts/install.sh
```

Sem dependências novas além de `golang.org/x/{sys,text}`. Texto pro usuário em português, prompts
e comentários em inglês. Vulnerabilidades: [`SECURITY.md`](SECURITY.md), nunca em issue pública.

## Licença

[MIT](LICENSE). Partes derivadas de outros projetos mantêm a licença original, em
[`third_party/`](third_party): o motor de edição do pi (MIT) e a skill `frontend-craft`/`ui_lint`
do Impeccable (Apache-2.0). Créditos e inspirações em [`CREDITS.md`](CREDITS.md).
