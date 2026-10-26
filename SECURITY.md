# Política de segurança

## O que o jazz pode fazer na sua máquina

O jazz executa comandos de shell e edita arquivos escolhidos por um modelo de linguagem. Leia antes de usar:

- No modo `auto` (padrão em pastas que você confiou), comandos não marcados como perigosos rodam sem perguntar. Em pastas desconhecidas o jazz pergunta antes de confiar.
- No modo `turbo` (`--turbo`, opt-in) o jazz deixa de perguntar sobre o que fica dentro do projeto e conta com o sandbox do sistema como barreira: pergunta só para escrita/remoção fora do projeto, envio de dados a host novo (uma vez por projeto), leitura de segredo, git destrutivo, pacote marcado pelo guardrail de supply chain, comando catastrófico, regra `ask`/hook e injeção suspeita no turno. Sem sandbox que isole (macOS `sandbox-exec`, Linux `bwrap`) ele aplica as regras do `auto`. Antes de executar alvos conhecidos de Make, npm/yarn/pnpm/bun, just, Task, Rake, Gradle, Maven, Cargo Make, tox ou nox, o jazz inspeciona o alvo e scripts locais que consegue resolver. Ações críticas bloqueiam em todos os modos; riscos altos pedem confirmação. A inspeção é heurística, não prova o comportamento de expressões dinâmicas, includes ou sintaxe não reconhecida, e não substitui sandbox. O que o turbo não protege: arquivos do projeto (o checkpoint shadow-git desfaz o que é versionável até 1 MB) e o conteúdo do projeto enviado a um host já aprovado. Cada liberação automática entra na auditoria (`auto_approval`, com a regra).
- O "sempre" (`a`) de uma pergunta com motivo de risco é guardado em `.jazz/approvals.json`, que só vale se foi o jazz que escreveu (hash no armazenamento de confiança): um repositório que já venha com esse arquivo é ignorado. Segredo, arquivo protegido, comando catastrófico, rug-pull de MCP e injeção nunca são guardados.
- Conteúdo de arquivos, saídas de comandos, páginas web e respostas de servidores MCP são tratados como dados, não como instruções, mas nenhum filtro é perfeito. Não aponte o jazz para repositórios em que você não confia sem o modo `ask`.
- Chaves de API ficam em `~/.config/jazz-cli/auth.json` (modo 0600). Elas são removidas do ambiente dos comandos e redigidas dos logs e sessões. Com o sandbox do sistema ativo (abaixo), nenhum comando consegue ler esse arquivo; sem ele, um comando malicioso pode ler qualquer arquivo do seu usuário.
- Provedores gratuitos podem usar seus prompts para treinamento. O jazz avisa uma vez; a escolha é sua.
- Não há telemetria: o jazz só fala com os provedores de modelo que você configurou, com servidores MCP que você cadastrou e, no `jazz update`, com a URL de releases.

## Redação de segredos antes do provedor (`redact`)

Ler caminhos de segredo (`.env`, chaves, `auth.json`) já é bloqueado, mas um segredo também pode estar dentro de um arquivo comum (um `config.yaml` com senha, um README com token, um log com `Authorization: Bearer`, um `git diff`, a saída de `env`/`printenv`/`cat`). Antes de uma requisição remota, os adaptadores compatíveis com OpenAI (kilo, omni, OpenRouter, Gemini), Anthropic e Codex redigem o sistema, mensagens, argumentos de chamadas e metadados das ferramentas (descrições e schemas). Isso também cobre requisições de subagentes, hedge/painel/verificador e compactação, desde que passem por esses adaptadores (`redact.go`):

- Segredos de alta confiança viram marcadores estáveis, `<secreto:aws-key#1>`: o mesmo valor ganha o mesmo marcador durante a sessão. O mapa reverso fica só na memória do processo (nunca em disco).
- Quando o modelo usa um marcador numa chamada `bash`, `write` ou `edit`, o jazz põe o valor real antes de checar e rodar a chamada localmente; o modelo pode "usar" um token num comando sem nunca vê-lo. A saída volta a ser redigida antes de ir pro modelo. Ferramentas de rede (`fetch`, MCP) e subagentes recebem o marcador, não o valor.
- Detectado (regex + entropia, inspirado no gitleaks/detect-secrets, curado para poucos falsos positivos): chaves AWS (`AKIA…`, secret com nome de chave), conta de serviço GCP (`private_key_id`), Google API/OAuth, Azure (AccountKey, SAS `sig=`, client secret `…Q~…`), GitHub (`ghp_`, `gho_`, `ghs_`, `github_pat_`), GitLab (`glpat-`…), Slack (tokens e webhooks), Discord webhook, Stripe, Twilio, SendGrid, OpenAI/Anthropic/OpenRouter/Groq/xAI/HF/npm, JWT, blocos PEM de chave privada (inclusive SSH e dentro de JSON), `Authorization: Bearer/Basic`, URL com usuário:senha (e strings de conexão de banco), `password=`/`senha=`/`secret=`/`token=` com valor não trivial, `.env` `*_KEY/*_SECRET/*_TOKEN/*_PASSWORD` com valor de alta entropia, e os valores exatos das suas credenciais locais (variáveis de chave do ambiente, `auth.json`).
- Nunca redigido: hashes do git e digests, UUIDs, chaves públicas e certificados, marcadores já presentes, referências (`$VAR`, `${…}`, `os.Getenv(…)`, `cfg.Password`) e fixtures obviamente falsas (`CANARY`, `example`, `changeme`, `your_…`, `xxxx`, um caractere repetido). Os canários do red-team não são redigidos de propósito: um vazamento continua aparecendo igual no sumidouro.
- Provedor na própria máquina (Ollama, llama.cpp, LM Studio em `localhost`) recebe o texto sem redação. A saída mostrada para você (resposta, `-p`, `--output-format json`) não é redigida por essa fronteira; a saída de ferramenta é redigida antes de chegar ao modelo.
- `redact: on|off|strict` na config, `--redact`, `JAZZ_REDACT` (padrão `on`). `strict` também redige valores de média confiança (senha que é uma palavra só, nomes camelCase) e sequências longas de alta entropia sem nome de chave por perto. `/redact` mostra o estado e as contagens; `/redact on|off|strict` muda na sessão.
- Falso positivo: `/redact allow <secreto:tipo#n>` (digitado por você) libera aquele valor neste projeto. O `.jazz/redact-allow.json` guarda só um HMAC do valor com uma chave desta máquina (`~/.config/jazz-cli/redact.key`, que o agente não pode ler): um repositório que já venha com esse arquivo não libera nada, e o modelo não consegue acrescentar entradas.
- Visibilidade: na primeira vez, um aviso ("1 segredo não saiu da máquina"); o total do turno mostra "N segredos retidos"; eventos `redact.hit {kind, where}` no `jazz trace` e registros `redact` na auditoria, sempre só com o tipo, nunca o valor.
- Limites: é uma heurística de padrões e credenciais locais conhecidas, não uma garantia para qualquer valor secreto. Segredo sem forma reconhecível (uma senha curta sem nome de chave perto, um token proprietário sem prefixo) pode passar no modo `on`. O marcador restaurado dentro de aspas duplas no shell está sujeito à expansão do shell (`$` no valor). Uma sessão retomada (`--resume`) começa um mapa novo: marcadores antigos do histórico não voltam a valer.

## Sandbox do sistema (`--sandbox`)

Tudo que o jazz executa (shell, jobs em segundo plano, hooks, servidores MCP stdio, scripts) roda dentro de um sandbox do sistema operacional, por baixo das guardas do jazz:

- **macOS**: `sandbox-exec` (verificado). **Linux**: `bubblewrap` (`sudo apt install bubblewrap`; não verificado em máquina real ainda). **Windows**: só limites de job object (processos, área de transferência, UI), sem isolamento de arquivos.
- Dentro dele, os comandos não leem sua home (exceto a pasta do projeto e caches de ferramentas como Go, npm e Cargo), nem `~/.ssh`, `~/.aws`, `~/.gnupg`, chaveiros, perfis de navegador ou a config do jazz, mesmo que o projeto seja a sua home. Gravar só no projeto, numa pasta temporária própria e nos caches de pacote.
- `--sandbox auto` (padrão): usa o sandbox se houver; se não houver, roda sem e o `jazz doctor` avisa. `--sandbox strict`: recusa rodar sem sandbox e corta a rede dos comandos (só loopback). `--sandbox off`: desliga. Também `sandbox` / `sandbox_net` na config e `JAZZ_SANDBOX` / `JAZZ_SANDBOX_NET`.
- O sandbox não cobre as ferramentas internas de leitura e edição de arquivos nem o `fetch` (rodam dentro do jazz, protegidas pela cerca do projeto). Nos modos `auto` e `turbo` os comandos ainda têm rede: exfiltrar algo que eles conseguem ler (o próprio projeto, arquivos fora da home) continua possível. Para repositório desconhecido use `--sandbox strict` e o modo `ask`.
- Teste: `jazz doctor` (linha "sandbox"; `jazz doctor --sandbox-probe` lista o que o sandbox conseguiu ler, `-v` mostra o argv) e `jazz --sandbox strict -p 'echo ok'`.

## Auditoria (`jazz audit`)

O jazz mantém um log de auditoria só de acréscimo, encadeado por hash, em `~/.local/share/jazz-cli/audit/audit.jsonl`: a configuração em vigor a cada início (e toda mudança do `config.json`, por hash), decisões de confiança, toda ação bloqueada, negada ou liberada por confirmação, toda escrita/edição/remoção de arquivo (caminho e hash antes/depois, sessão e passo), todo comando de shell (hash e os primeiros 200 caracteres, redigidos), o primeiro acesso de rede a cada host e as atualizações aplicadas ou recusadas (com o estado da assinatura). Nenhum conteúdo de arquivo ou prompt entra nele.

- Verificar: `jazz audit --verify` (código 0 = cadeia íntegra; 1 = registro alterado, removido ou fora de ordem, com a linha).
- Ler/exportar: `jazz audit [--since 24h] [--kind file] [--json]`.
- Retenção: nunca apagado pelo jazz; acima de 16 MB é rotacionado sem quebrar a cadeia.
- Limite: protege contra edição pontual, não contra quem reescreve a cadeia inteira com acesso à sua home; exporte o último hash para fora da máquina se precisar dessa garantia.

Detalhes, formato e o rastreio de mudanças por arquivo (`jazz changes`): [docs/governance.md](docs/governance.md).

## Endurecimento 2 (determinístico, vale também em turbo e yolo)

- **Escape de container**: `docker|podman|nerdctl run|create` com `--privileged`, `--pid|--net|--ipc=host`, `--cap-add=SYS_ADMIN|ALL|SYS_PTRACE|SYS_MODULE`, `-v`/`--mount` de `/`, `/etc`, `/root`, da sua home, de `docker.sock` ou de chaves, `--device /dev/sd*`, `--security-opt seccomp|apparmor=unconfined` sempre pergunta; `docker exec -u 0|root` também. `docker compose up|run` e a escrita/edição de um arquivo compose com esses recursos pedem confirmação (`hardening2.go`).
- **Limites de recurso**: o shell persistente e o de leitura rápida começam com `ulimit -u` (processos: os atuais + 512; só se der para medir), `-f` (arquivo de até 2 GB), `-n` (4096) e, opcional, `-t` (CPU por processo); ajuste em `limits` (`procs`, `file_mb`, `files`, `cpu_secs`) na config. No Windows valem os limites de job object. Fork bomb, `yes > arq`, `dd if=/dev/zero of=`, `fallocate`/`truncate` enormes, `cat /dev/urandom > arq` e `while true` que grava em disco perguntam.
- **Anti-downgrade do `jazz update`**: recusa manifesto assinado com versão menor que a em execução ou menor que a última vista (guardada em `update_seen.json`, só avança) salvo `--allow-downgrade`; `jazz update --check` mostra o aviso; toda tentativa vai para a auditoria.
- **Integridade das sessões**: cada linha do `.jsonl` leva um HMAC-SHA256 (chave aleatória por instalação em `session.key`, 0600). Ao retomar, linhas alteradas ou sem assinatura depois de linhas assinadas são avisadas ("sessão alterada fora do jazz: N linhas") e carregadas como dados NÃO CONFIÁVEIS (mesma marca `<untrusted>` do conteúdo externo, sem chamadas de ferramenta). Retomada headless de sessão alterada sai com código 5, salvo `--allow-tampered`. Sessões antigas (sem assinatura) abrem com um aviso único.
- **Lições e plan cache**: lição ou plano com URL/IP/host, comando de rede ou de segredo, pedido para desligar guarda/sandbox/redação/modo ou frase imperativa dirigida ao agente ("sempre rode", "não pergunte", "ignore as instruções") nunca é gravado nem injetado.
- **Clone e instalação git**: `git clone`, `git submodule add`, `pip install git+…`, `npm i git+…|github:…`, `cargo install --git` de um host que o repositório não usa (remotes e `.gitmodules`) perguntam mostrando o host; a pasta clonada continua não confiada até `/trust`.
- **Escapes de terminal**: OSC 52 (área de transferência), OSC 8 (links `javascript:`/`file:`), OSC 0/2 (título), DCS/APC/PM e as versões de 8 bits (C1) são removidos de toda saída mostrada ao usuário.
## Revisão do próprio diff

No fim de cada turno o jazz revisa **os arquivos que ele mesmo mudou** (as escritas e edições do turno e o que o bash alterou, pelo checkpoint do início do turno), como um linter antes do "pronto". Só as linhas **adicionadas** contam; o que já estava no arquivo não é cobrado. Se aparece um achado CRITICAL ou HIGH, o jazz mostra o trecho (tecla `v`) e pergunta antes de aceitar:

- **Sim**: as mudanças ficam. **Sempre**: grava um hash de (regra, arquivo, texto da linha) em `.jazz/scan-allow.json` e a mesma linha não é perguntada de novo neste projeto. Como o `approvals.json`, o arquivo só vale se foi o jazz que o escreveu (store de confiança, tipo `scan-allow`) e as ferramentas do agente não conseguem gravá-lo.
- **Não**: os arquivos sinalizados voltam ao que eram no início do turno, o jazz avisa o que restaurou e o modelo recebe a nota de que não deve refazer sem perguntar. O turno termina como negado.
- **Headless (`-p`)**: ninguém aprova, então os arquivos sinalizados são restaurados, os achados vão para `.jazz/scan-findings.json` (regra, arquivo, linha, hash, motivo, trecho com segredos redigidos) e o código de saída é 5 (`StopDenied`).
- MEDIUM e abaixo só viram um aviso (em `strict` também perguntam). Toda ocorrência entra na auditoria como `diffguard.finding {rule, file, line, hash}` e a resposta como `approval` com `tool: diffguard`.
- Config `diff_guard: on|off|strict` (padrão `on`), `--diff-guard on|off|strict`, `JAZZ_DIFF_GUARD`. Vale em todos os modos de permissão, inclusive `yolo` e `turbo`. `jazz scan [--since ref]` roda o mesmo motor sob demanda (sem `--since`: árvore de trabalho contra `HEAD`, arquivos novos incluídos; sai com 5 se houver CRITICAL/HIGH) e `jazz scan --rules` lista os pacotes e regras carregados.

**Regras embutidas** (`diffrules.json`):

| Regra | Severidade | O que pega |
|---|---|---|
| `jz-secret-literal` | CRITICAL | chave, token ou senha literal (o mesmo detector de `redactSecrets`) |
| `jz-tls-off` | HIGH | `verify=False`, `InsecureSkipVerify: true`, `rejectUnauthorized: false`, `NODE_TLS_REJECT_UNAUTHORIZED=0`, `curl -k`/`--insecure`, `StrictHostKeyChecking no` e parentes |
| `jz-dep-url` | HIGH | dependência de URL git, tarball ou endereço fora do registro |
| `jz-dep-registry` | HIGH | registro/índice que não é um dos públicos oficiais |
| `jz-dep-typosquat` | HIGH | dependência nova a distância 1-2 de um pacote popular (mesma lista do guarda de pacotes, só pelo nome) |
| `jz-lifecycle-script` | HIGH | `preinstall`/`install`/`postinstall`/`prepare` no `package.json`, `cmdclass` no `setup.py` |
| `jz-ci-change` | HIGH | qualquer mudança em `.github/workflows`, `.gitlab-ci.yml`, `Jenkinsfile`, `azure-pipelines*`, `.circleci`, `bitbucket-pipelines` |
| `jz-persist-outside` | HIGH | escrita fora do projeto em arquivo de shell, cron ou inicialização (já exige confirmação da cerca; aqui aparece no resumo do turno) |
| `jz-hidden-unicode` | MEDIUM | caracteres invisíveis, bidi e largura zero em linha nova (mesmo detector do `sanitize`) |

**Pacotes de regras.** Regras são dados. Além das embutidas, o jazz lê `~/.config/jazz-cli/diffrules.d/*.json` e `<projeto>/.jazz/diffrules.d/*.json`, nessa ordem; uma regra com o mesmo `id` de uma anterior a substitui, e `"disabled": true` desliga a regra com aquele `id`. Os pacotes do projeto só carregam depois que você confia no conteúdo exato (`jazz trust`, tipo `diffrules`; editou, pede de novo) e as ferramentas do agente não escrevem nessa pasta. Um arquivo é `{"rules":[...]}` ou uma lista direta:

```json
{
  "rules": [
    {
      "id": "acme-aws-key",
      "severity": "CRITICAL",
      "globs": ["**/*.py", "src/**"],
      "exclude": ["**/tests/**"],
      "regex": "AKIA[0-9A-Z]{16}",
      "message_pt": "chave de acesso AWS escrita no código",
      "why": "A chave fica no histórico do git; use variável de ambiente."
    }
  ]
}
```

| Campo | Significado |
|---|---|
| `id` | nome único da regra (obrigatório) |
| `severity` | `CRITICAL`, `HIGH`, `MEDIUM`, `LOW` ou `INFO`; CRITICAL e HIGH perguntam |
| `globs` | arquivos que a regra olha; vazio = todos. `*` fica dentro de uma pasta, `**` atravessa pastas, sem `/` casa o nome do arquivo em qualquer lugar |
| `exclude` | arquivos que a regra pula (mesma sintaxe) |
| `scope` | `project` ou `outside` (fora do projeto); vazio = os dois |
| `regex` | expressão RE2 testada em cada linha adicionada. Sem `regex` e sem `detector` a regra vale para qualquer mudança nos arquivos dos `globs` |
| `detector` | alternativa à `regex`, um detector embutido: `secret`, `unicode`, `registry`, `typosquat` |
| `message_pt` | texto mostrado ao usuário (obrigatório) |
| `why` | uma frase sobre o risco |
| `disabled` | `true` desliga a regra com esse `id` |

Para portar regras de conjuntos públicos: do gitleaks, `id`→`id`, `regex`→`regex`, `path`→`globs` (e `allowlist.paths`→`exclude`); do semgrep, só as regras `pattern-regex` / `languages` simples têm equivalente (`pattern-regex`→`regex`, extensão da linguagem→`globs`, `severity` ERROR→CRITICAL, WARNING→HIGH, INFO→MEDIUM). Regras com `lookahead` ou backreference não existem em RE2 e precisam ser reescritas. Regras estruturais do semgrep rodam pelo semgrep de verdade, abaixo.

**semgrep opcional.** Se `semgrep` está no PATH (`pip install semgrep`; o `jazz doctor` mostra se foi achado), o jazz roda `semgrep --config p/security-audit --config p/secrets --json` nos arquivos alterados, com limite de 60 s, e guarda só os resultados que caem em linhas adicionadas: ERROR vira CRITICAL, WARNING vira HIGH, INFO vira MEDIUM (id `semgrep:<check_id>`). Sem rede e sem os pacotes de regras em cache, o jazz avisa uma vez e segue só com as regras embutidas.

Limites: é revisão por padrões (regex), não análise de fluxo; quem escreve o código de forma indireta (`getattr`, string montada) passa. Só cobre o que o jazz mudou no turno, não commits e edições suas.  Arquivos fora do projeto mudados por comandos de shell não são vistos (só os das ferramentas de escrita/edição).

## Versões suportadas

Apenas a última versão publicada recebe correções.

## Relatando uma vulnerabilidade

Abra um *security advisory* privado no GitHub do projeto ou escreva para o e-mail indicado no perfil da organização. Descreva o cenário e, se possível, um comando ou arquivo que reproduza o problema. Respondemos em até 7 dias.
