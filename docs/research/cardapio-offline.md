# Cardápio offline: biblioteca completa de scripts determinísticos

O cérebro offline raciocina sobre DADO ESTRUTURADO; quem produz o dado são scripts
determinísticos do cardápio (`jazz-scripts/`, `scripts > prompts`). Cada script é um
asset forjado uma vez e reusado pra sempre, zero token: sidecar com schema de args e
efeito declarado, `lib/core.py` (`emit/fail`, `path_in_root`, orçamentos, `truncated`),
stdlib puro, fixture em `testdata/`, teste Go que roda pelo mesmo caminho do jazz.
Regras de entrada: `py_compile`/`shellcheck` limpos; saída ordenada e sem randomness;
nunca sair da raiz; nunca imprimir valor de segredo; efeito honesto (`read` só lê;
`exec` só consulta; mutação só com `Permit`); o brain **instancia** scripts, nunca
gera shell livre. O runtime compõe (HTN), instancia templates (`script_gen`) e cunha
macro-operadores por frequência (MDL); autoria aberta é da forja (LLM, offline-time).

## v0 — embarcados (20)
api_map, csv_stats, detect_project, diff_quality, extract_stacktrace, extract_versions,
find_symbol, git_safe, git_state, http_check, json_edit, parse_build_errors,
parse_log_lines, parse_test_output, port_free, run_tests, snapshot_cmd, tree_summary,
ui_lint, yaml_get.

## v1 — localização, sensores e oráculos (8)
git_cochange (co-mudança/churn → localize), todo_scan, proc_ports (/proc, sem ps/ss),
disk_usage (statvfs, sem du/df), file_hash_diff (oráculo: mudou?), service_status
(oráculo: subiu/caiu?), config_get (json/yaml/toml/ini/env; superconjunto do yaml_get),
dotenv_check (nunca imprime valores).

## v2 — código, git e build (10)
git_blame_hot (quem/quando por faixa; hotspots) · git_branch_status (ahead/behind,
órfãs, sem merge) · git_conflict_scan (marcadores de conflito) · dep_graph (grafo de
imports py/go/js) · dep_outdated (manifesto × lockfile, sem rede) · lint_run (gofmt/vet/
ruff/eslint se existirem; saída parseada) · symbol_refs (usos de um símbolo, sem rg) ·
large_funcs (funções > N linhas, como dado) · dup_code (blocos quase-duplicados por hash
normalizado) · license_scan (LICENSE/SPDX/cabeçalhos).

## v3 — sistema e ops (10)
env_tools (toolchains/binários + versões da máquina) · mem_top (/proc/meminfo + RSS por
processo via statm) · net_ifaces (/proc/net/dev, /sys/class/net — sem `ip`) ·
log_errors_recent (journalctl/syslog só leitura) · cron_list (crontab/timers só leitura) ·
fs_recent (modificados nos últimos N min) · archive_list (zip/tar sem extrair) ·
hash_verify (arquivo × .sha256/.md5) · pkg_installed (dpkg/rpm/pip/npm instalados,
só leitura) · uptime_load (/proc/uptime, /proc/loadavg, /proc/stat).

## v4 — texto e dados (10)
text_grep (grep com contexto e orçamento, sem rg) · json_schema_infer · md_outline
(títulos/links/âncoras quebradas) · diff_apply_check (patch aplica? dry-run) ·
regex_test (testa regex contra linhas, com timeout) · date_normalize · url_parse ·
csv_to_json · table_render (dado → tabela alinhada) · wordcount_stats.

### Contrato implementado da v4

Os dez scripts recebem `file` (relativo à raiz) ou `text` inline; a exceção é
`diff_apply_check`, que exige um arquivo de patch. A integração offline pede um
arquivo obrigatório. Exemplos: `conta palavras README.md`, `sumario markdown
README.md`, `busca literal README.md "ana"`, `testa regex README.md "^ana$"`.

- `text_grep`: busca literal, contexto de 0 a 10 linhas e limite de resultados.
- `json_schema_infer`: esquema dos valores observados; arrays heterogêneos usam
  `anyOf`. Não presume que uma amostra descreva todos os documentos possíveis.
- `md_outline`: títulos ATX/Setext, links inline e âncoras locais quebradas;
  ignora blocos cercados por crases ou tils.
- `diff_apply_check`: somente `git apply --check`; `applies:false` significa
  incompatibilidade, `null` significa timeout. Não grava arquivos.
- `regex_test`: padrão de até 512 caracteres, até 1.000 linhas de 4.096
  caracteres, 25 ms por busca e orçamento total de aproximadamente 4 s.
  Exige temporizador POSIX; timeout produz `matched:null`, nunca falso negativo.
- `date_normalize`: ISO 8601 por padrão, ou `format` explícito de `strptime`;
  converte offsets para UTC e conserva datas sem fuso como ingênuas. Inválido = null.
- `url_parse`: componentes e parâmetros repetidos, sem rede; aceita referências
  relativas e não inventa esquema, host ou porta ausentes.
- `csv_to_json`: preserva valores como strings e ordem das linhas, rejeitando
  cabeçalhos duplicados e larguras inconsistentes; separador configurável.
- `table_render`: array JSON de objetos, colunas ordenadas, ausências como null.
- `wordcount_stats`: linhas, caracteres, palavras Unicode e frequências ordenadas.

Entrada limitada a 256 KiB, com `truncated:true` quando há corte. JSON e patches
incompletos são recusados. Caminhos resolvidos (inclusive symlinks) ficam na raiz;
saídas incluem `data.text`, dados estruturados e `data.truncated`.

## v5 — finders e inspeção (10; assets forjados, ligação com o mantenedor)
file_find (nome/ext/tamanho/mtime/tipo/vazio) · text_extract (ipv4/email/url/
data/número/hex/uuid ou regex) · line_slice (head/tail/faixa de linhas) ·
file_dupes (arquivos idênticos por sha256; distinto de dup_code, que acha
trechos de código parecidos) · perm_find (modo octal, suid/sgid,
world-writable, executável, dono) · env_get (variáveis de ambiente; nomes com
cara de segredo nunca mostram valor) · file_users (quem segura path aberto via
/proc/*/fd, path exato resolvido) · net_route (tabela de rotas v4/v6 do
/proc/net/route*) · text_count (ocorrências de literal/regex por linha) ·
dir_diff (duas árvores: só-em-a, só-em-b, conteúdo divergente — a metade de
leitura do rsync --dry-run).

Alvo escolhido pela medição `docs/research/coverage-v5.md`: as duas maiores
classes de miss do corpus (find de arquivos, processamento de texto) mais os
falsos matches que um oráculo certo desarma (dup de arquivo, permissão,
variável de ambiente, rota, "quem tem o arquivo aberto").

## Ordem e portão
Uma onda por vez: 4 agentes × 2-3 scripts → revisão do maestro (linha a linha) →
roster `scShipped` em scripts_test.go → `py_compile` + testes + `eval offline` (75/75)
→ gate na máquina fraca (eval antes da suíte; instalação em passo separado). Métrica: cobertura
de classes de pedido real resolvidas por script (sem LLM), `false_success` = 0.
