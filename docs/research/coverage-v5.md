# Cobertura do cérebro offline — medição v5 (nov/2026)

Medição em corpus real, não estimativa. Todos os números vêm de execuções do
binário compilado deste branch (`CGO_ENABLED=0 go build`), um pedido por vez,
`JAZZ_OFFLINE=1 jazz -p --offline --timeout 20s "PEDIDO" </dev/null`, HOME
descartável e diretório de trabalho = fixture descartável (projeto Go misto com
logs, csv, configs, zip, git). Intent/status lidos dos eventos da sessão
(`data/sessions/*/*.events.jsonl`); a primeira linha da resposta foi gravada e
cada pedido entendido teve veredito manual (right / partial / false).

## Corpus (372 pedidos, estratificado)

| fonte | n | o que é |
|---|---|---|
| nl2bash | 150 | frases reais do dataset NL2Bash (TellinaTool/nl2bash, all.nl), amostra estratificada por classe sintática (file-find, text-proc, file-ops, net, proc, disk-fs, perm, pkg, git, compress, sched, sys-info, misc) |
| tbench | 67 | paráfrases dos nomes/instruções públicos das tarefas do Terminal-Bench 2 (sweng, sysadmin, security, data, debug, fileops, sci, ml, games, other) |
| chores | 155 | tarefas dev/ops do dia a dia escritas à mão — 78 PT + 77 EN, mesmas categorias nos dois idiomas |

## Manchete

| métrica | n | % |
|---|---|---|
| **entendeu** (intent escolhida) | 145/372 | **39,0%** |
| recusou (não entendeu, resposta honesta) | 227/372 | 61,0% |
| dos 145 "entendidos": intent certa | 53 | 36,6% |
| dos 145: intent da família certa, resposta incompleta | 19 | 13,1% |
| dos 145: **intent errada (falso match)** | **73** | **50,3%** |

Ou seja: a cobertura nominal (39%) superestima a real em ~2× — cobertura
efetiva é ~19% (72/372). E **metade do que o cérebro "entende" está errado**;
dos 73 falsos matches, **34 responderam errado sem perguntar nada**
(status=succeeded) — esses são os perigosos, não as recusas.

Latência da sonda: p50 ~2,8 s, p95 ~6,7 s (inclui boot do processo e
instanciação do script; não é o p50 do brain em si).

## Cobertura por categoria

`ok` = entendeu; `right`/`partial`/`false` = veredito dos entendidos.

| categoria | ok/n | % | right | partial | false |
|---|---|---|---|---|---|
| nl-file-find | 6/30 | 20,0% | 1 | 2 | 3 |
| nl-file-ops | 3/18 | 16,7% | 2 | 0 | 1 |
| nl-text-proc | 5/22 | 22,7% | 0 | 0 | 5 |
| nl-misc | 2/12 | 16,7% | 1 | 1 | 0 |
| nl-sys-info | 2/10 | 20,0% | 1 | 0 | 1 |
| nl-net | 5/14 | 35,7% | 1 | 0 | 4 |
| nl-disk-fs | 0/9 | 0,0% | 0 | 0 | 0 |
| nl-perm-owner | 3/7 | 42,9% | 0 | 0 | 3 |
| nl-proc | 3/9 | 33,3% | 1 | 2 | 0 |
| nl-compress | 1/7 | 14,3% | 0 | 0 | 1 |
| nl-pkg-env | 3/6 | 50,0% | 0 | 3 | 0 |
| nl-git | 0/4 | 0,0% | 0 | 0 | 0 |
| nl-sched | 0/2 | 0,0% | 0 | 0 | 0 |
| tb-sweng | 2/10 | 20,0% | 0 | 1 | 1 |
| tb-sysadmin | 0/10 | 0,0% | 0 | 0 | 0 |
| tb-security | 1/8 | 12,5% | 0 | 0 | 1 |
| tb-data | 0/9 | 0,0% | 0 | 0 | 0 |
| tb-debug | 3/7 | 42,9% | 1 | 1 | 1 |
| tb-fileops | 1/5 | 20,0% | 0 | 0 | 1 |
| tb-sci | 0/5 | 0,0% | 0 | 0 | 0 |
| tb-ml | 0/5 | 0,0% | 0 | 0 | 0 |
| tb-games | 0/4 | 0,0% | 0 | 0 | 0 |
| tb-other | 1/4 | 25,0% | 0 | 0 | 1 |
| chore-files | 11/17 | 64,7% | 3 | 3 | 5 |
| chore-text | 6/16 | 37,5% | 0 | 0 | 6 |
| chore-git | 12/16 | 75,0% | 8 | 0 | 4 |
| chore-pkg | 10/10 | 100,0% | 8 | 2 | 0 |
| chore-svc | 8/10 | 80,0% | 2 | 0 | 6 |
| chore-docker | 8/10 | 80,0% | 2 | 4 | 2 |
| chore-net | 11/14 | 78,6% | 2 | 0 | 9 |
| chore-sys | 6/10 | 60,0% | 3 | 0 | 3 |
| chore-proc | 9/10 | 90,0% | 6 | 0 | 3 |
| chore-disk | 5/10 | 50,0% | 4 | 0 | 1 |
| chore-arch | 1/6 | 16,7% | 0 | 0 | 1 |
| chore-perm | 2/6 | 33,3% | 0 | 0 | 2 |
| chore-env | 4/6 | 66,7% | 2 | 0 | 2 |
| chore-logs | 4/4 | 100,0% | 2 | 0 | 2 |
| chore-sec | 3/4 | 75,0% | 0 | 0 | 3 |
| chore-db | 0/2 | 0,0% | 0 | 0 | 0 |
| chore-misc | 4/4 | 100,0% | 3 | 0 | 1 |
| **total** | **145/372** | **39,0%** | **53** | **19** | **73** |

Por fonte: chores 67,1% (104/155), nl2bash 22,0% (33/150), tbench 11,9% (8/67).
Por língua: **pt 70,5%** (55/78) × **en 30,6%** (90/294) — a gramática é PT-first;
os exemplos em EN são os que ela já conhece.

## Top classes não cobertas (os 227 misses, agrupamento por palavra-chave — aproximado)

| n | classe |
|---|---|
| 47 | find de arquivos (nome/ext/tamanho/mtime/permissão/vazio/dono) |
| 36 | processamento de texto (grep/sed/awk, split, sort, uniq, head/tail, replace) |
| 36 | sem padrão claro (requests idiossincráticos do nl2bash, variáveis $1..$3, aliases, cal, seq) |
| 32 | tarefas abertas de bench (implementar, consertar, compilar, treinar, recuperar, converter — trabalho de LLM, não de oráculo) |
| 21 | operações de arquivo (copiar/mover/sincronizar — mutação; a metade de leitura é dir_diff) |
| 16 | info de proc/sys (usuário atual, suid, variável de ambiente, uname) |
| 12 | git (fetch, disk usage do fs, relógio de comando — vários sem intent clara) |
| 9 | arquivos compactados (criar/extrair tar/zip — mutação; listar já existe) |
| 7 | serviços/systemd/docker (failed no boot, habilitar — falta capability) |
| 6 | rede (ssh tunnel, download, ping, dns — fora do escopo offline) |
| 5 | pacote de sistema (apt/rpm install — mutação, fora do cardápio) |

O que um script determinístico só-leitura resolve desta lista: as duas primeiras
classes inteiras (~83 pedidos), a metade de leitura das file-ops (dir_diff), e
fatias de sys-info (env_get, file_users), net (net_route) e sec/perm (perm_find).
Fora de alcance honesto: mutações (copy/move/install/mkdir), rede ativa
(ssh/ping/download), e as tarefas abertas de bench — essas são do LLM, e a
recusa já é o comportamento certo.

## Falsos matches — o achado principal (73)

`veredito=false`: a intent escolhida não responde o pedido. **34 entregaram a
resposta errada sem perguntar** (status=succeeded), 24 caíram em needs_input,
5 falharam, 10 foram bloqueadas pelo gate.

### Por intent erroneamente escolhida

open_file 10 · list_files 9 · net_ifaces 7 · service_logs 5 · connect_host 4 ·
list_hosts 4 · project_summary 4 · env_status 3 · git_diff 3 · git_status 3 ·
service_start 3 · tool_info 3 · fs_recent 2 · create 2 · log_errors_recent 1 ·
help 1 · build 1 · delete_file 1 · find 1 · container_stop 1 · run_tests 1 ·
dup_code 1 · container_up 1 · list_ports 1 · archive_list 1.

### Os 34 que responderam errado sem perguntar (amostra fiel, sem valores do host)

- "discard all the errors and search for the file program.c" → **log_errors_recent** (contou erros do journal)
- "Locate all readme.txt files under the home directory" → **fs_recent** (listou recentes)
- "delete all text files in the entire file system" → **env_status** (mostrou o perfil da máquina)
- "Print a list of unique users who are logged in" → **list_hosts** (listou hosts tailnet)
- "Turns on network interface eth0" → **net_ifaces** (listou ifaces — mutação respondida com leitura)
- "Creates alias for network interface eth0 with IP…" → **net_ifaces**
- "Request IP address of 'myip.opendns.com' from name server…" → **net_ifaces**
- "Change to the directory containing the oracle executable" → **git_diff**
- "Recursively change the group ownership to laravel" → **git_diff** ×3 casos chown/chgrp→git_diff
- "View gzip's help documentation" → **help** (ajuda do jazz, não do gzip)
- "extrai todos os ips do access.log" → **net_ifaces** (também en-012 em EN)
- "mostra quem editou o main.go por último" → **open_file** (git_blame existia!)
- "quais arquivos estão modificados" → **fs_recent** (mtime-recente ≠ git-modificado)
- "lista as tags do repo" → **git_status**
- "mostra o status do sshd" → **git_status** (×2 idiomas: "show sshd status" idem)
- "qual meu ip externo" → **net_ifaces** (if locais ≠ ip público; en-040 idem)
- "mostra as rotas da máquina" → **env_status**
- "qual a versão do kernel" → **tool_info** ("kernel-install instalado"; en-048 idem)
- "lista os processos do usuário" → **list_hosts** (en-054 idem)
- "find duplicate files by content" → **dup_code** ("0 duplicate groups" — dup_code acha clones de código, não arquivos idênticos)
- "show empty directories" / "world-writable files" / "which folder is the heaviest" / "scaffold the project" → **project_summary**
- "check if port 443 on the proxy responds" → **list_ports** (listeners locais ≠ probe remoto)
- "how much ram does this machine have" → **tool_info** ("mactime instalado")
- "chown the data folder to the current user" → **env_status**

### Classes sistemáticas de bug e a correção de gramática proposta

1. **Slot faminto (o padrão documentado — "a palavra que nenhuma regra explicou")**.
   open_file e list_files encheram o slot file/dir com qualquer palavra livre:
   'lines', 'strings', 'calendar', 'shell', 'routing', 'has', 'environment',
   'variáveis', 'permissão', 'suid', 'world-writable', 'extrai', 'largest',
   'modified', 'current', 'last' — ~17 casos (a maioria dos 10+9).
   **Correção**: em open_file/list_files, o slot só aceita token que resolve a
   path existente, `@file`/`@dir` ou nome entre aspas; palavra-inexplicada não
   enche slot — ela derruba a confiança abaixo de nuMinConf (recusa honesta).
2. **service_* aceita qualquer palavra como unidade** ('tail', 'often',
   'segfault', 'kernel' viraram `journalctl -u <palavra>`; 'habilita' e 'enable'
   viraram alvo de `systemctl start`/`docker start`). 7 casos.
   **Correção**: slot de serviço só aceita @service/unidade conhecida; sem
   resolução, needs_input ou recusa — nunca inventa o nome da unidade.
3. **'ip' sozinho dispara net_ifaces** (7 casos: extrair ips de arquivo, ip
   externo, DNS lookup, iface up/alias).
   **Correção**: net_ifaces exige conceito de interface/rede; 'ip' dentro de
   contexto de extração/arquivo veta. 'ip externo/público' é irrespondível
   offline → recusa honesta, não lista de ifaces.
4. **Verbo de mutação respondido com leitura** (delete→env_status,
   chown→env_status, turn on/alias→net_ifaces, scaffold→project_summary,
   habilita→service_start): 8+ casos, os mais perigosos.
   **Correção**: conceito de mutação (delete, remove, muda, change, cria, gera,
   habilita, liga, turn on, chown, chmod) deve vetar TODA intent só-leitura —
   se só casa leitura e o verbo muta, recusa (errar pra menos, nunca pra mais).
5. **connect_host por 'conectada/context/shell/vpn'** (4 casos).
   **Correção**: exige @host ou conceito ssh explícito; 'vpn', 'container',
   'context' vetam; pergunta de estado ("está conectada?") ≠ pedido de conexão.
6. **list_hosts por 'user/hostname/usuário'** (4 casos: users logados,
   hostname→variable, processos do usuário ×2).
   **Correção**: 'user'/'hostname' não são hosts; exige conceito host/node.
7. **project_summary como fallback de 'mostra X'** (4 casos: empty dirs,
   heaviest folder, world-writable, scaffold).
   **Correção**: exige conceito projeto/resumo; 'show/list' + substantivo de
   filesystem não satisfaz.
8. **git_diff por 'change'** (3 casos chown/chgrp/cd).
   **Correção**: exige conceito git (diff, commit); 'change/muda' sozinho não basta.
9. **git_status por 'status do X'** ('sshd' ×2 idiomas, 'tags').
   **Correção**: exige contexto de working tree (git/repo/branch); nome de
   serviço + status → service_status; 'tags' → recusa ou intent própria.
10. **tool_info trata substantivo de sistema como binário** ('kernel'→kernel-install,
    'ram'→mactime; ×3).
    **Correção**: slot @tool só resolve nome de ferramenta conhecida;
    kernel/ram/distro são fatos de sistema (env_status), não ferramentas.
11. **env_status ampla demais** (delete-files, chown, rotas): ver #4 + 'rotas'
    deve mapear pra net_route (script novo).
12. **Objeto errado na família certa**: dup_code para "duplicate files"
    (file_dupes é o oráculo certo), fs_recent para 'locate readme.txt' e
    'arquivos modificados' (file_find / git_status).
    **Correção**: conceito se divide pelo substantivo — files≠code,
    locate≠recent, modificado(git)≠modificado(mtime).
13. **Singletons**: 'recover deleted'→delete_file (veto 'recover'),
    'filesystem image'→container_stop ('image' sozinha não basta),
    'transcribe audio'→run_tests (exige conceito test),
    'find the bottleneck'→find (exige símbolo),
    'make a rust file'→build ('make a file'→create),
    'gzip help'→help (tool+help→tool_info ou recusa),
    'arquivo mais novo'→create ('mais novo' superlativo não é 'new'),
    'baixa o release'→create ('baixa/download' veta create),
    'port 443 on the proxy'→list_ports (alvo remoto veta listeners locais),
    'quem editou X por último'→open_file (conceito blame→git_blame).

Dois meta-fixes resolvem a maior parte: **slots nunca aceitam palavra
inexplicada** e **verbo de mutação veta intent só-leitura**. Juntos cobrem
~25 dos 73 falsos matches e quase todos os "respondeu errado sem perguntar".

## Cardápio v5 (parte 2 desta onda)

10 scripts novos, mesmo contrato, para as classes do topo:
file_find · text_extract · line_slice · file_dupes · perm_find · env_get ·
file_users · net_route · text_count · dir_diff.
Não ligados ainda (nlu/methods/brain ficam com o mantenedor): este branch só
mede e forja os assets.
