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

## Ordem e portão
Uma onda por vez: 4 agentes × 2-3 scripts → revisão do maestro (linha a linha) →
roster `scShipped` em scripts_test.go → `py_compile` + testes + `eval offline` (75/75)
→ gate no d610 (eval antes da suíte; instalação em passo separado). Métrica: cobertura
de classes de pedido real resolvidas por script (sem LLM), `false_success` = 0.
