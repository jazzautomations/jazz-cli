# Governança: auditoria e rastreabilidade

## Log de auditoria

**Onde:** `~/.local/share/jazz-cli/audit/audit.jsonl` (`$JAZZ_HOME/data/audit/` quando definido),
modo 0600, uma pasta por usuário da máquina. Separado do log de debug (que é opcional, rotativo e
guarda payloads); a auditoria é sempre ligada, pequena e nunca guarda conteúdo, só hashes.

**Formato:** uma linha por registro,

```json
{"prev":"<sha256>","hash":"<sha256>","rec":{"seq":12,"ts":"2026-10-21T00:49:52.1Z","kind":"shell","sid":"261020-214357-15e7","step":3,"pid":92690,"data":{...}}}
```

`hash = sha256(prev + "\n" + rec)` sobre os bytes exatos de `rec`; o primeiro registro tem `prev` =
64 zeros. Todos os processos jazz do usuário escrevem na mesma cadeia sob trava de arquivo
(`audit.lock`), então a ordem é global e `seq` é contínuo.

**O que é registrado:**

| kind | quando | dados |
|---|---|---|
| `config` | início de toda sessão (interativa e `-p`) | versão, pasta, modelo, modo, guardas, sandbox, block_ux, fora-do-projeto, orçamento, recover, allow_tools, timeout, `config_sha` (do config.json) e `effective_sha` (de tudo isso) |
| `config.changed` | o config.json mudou desde o último início (ou primeira vez) | `before`/`after` (sha256) |
| `trust` | `jazz trust`, `/trust`, revogação | ação, pasta, tipos, sha256 do conteúdo confiado |
| `action.refused` | toda chamada bloqueada ou negada (permissão, hook, guardas honestas, orçamento, disjuntor) | guarda, ferramenta, alvo (200 chars, redigido), motivo, resultado, sha dos argumentos |
| `approval` | toda confirmação respondida (inclusive as liberadas: são os *overrides*) | ferramenta, alvo, motivo, resposta |
| `file.change` | todo arquivo criado/editado/apagado | caminho, operação, via (write/edit/bash), `before`/`after`: `sha256:` (write/edit) ou `git:` blob (bash, pelo diff do checkpoint) |
| `shell` | todo comando bash executado | sha256 do comando inteiro + primeiros 200 chars (redigidos), erro, duração |
| `net.egress` | primeira requisição do processo a cada host (provedor, fetch, update) | host, via |
| `update` | `jazz update` instalou ou recusou | de/para, assinatura (ok/refused), sha256, resultado |
| `run.end` | fim de uma execução `-p` | status, situação, código de saída, checkpoint |
| `lesson.applied` | `jazz improve --apply` ou `improve.auto` gravou uma lição ([improve.md](improve.md)) | arquivo, via (apply/auto), tag, lição, sessão de origem, sha256 da linha |

**Verificar:** `jazz audit --verify` percorre todos os arquivos e confere hash, elo anterior e
sequência. Saída `✓ cadeia íntegra: N registros` (código 0) ou `✗ cadeia QUEBRADA` com a linha de
cada problema (código 1): registro alterado (hash), removido/inserido/reordenado (elo), lacuna de
sequência. Uma linha cortada por crash aparece como "cortada (fora da cadeia)" e não quebra a cadeia.

**Ler e exportar:** `jazz audit` (todas, em português), `--since 24h|7d|2026-10-20`, `--kind file`
(prefixo: `file`, `config`, `action`…), `--json` (as linhas cruas, prontas para enviar a um SIEM ou
guardar fora da máquina). Os arquivos são JSONL simples: copie-os.

**Retenção:** o jazz nunca apaga a auditoria. Acima de 16 MB o arquivo atual vira
`audit-<último seq>.jsonl` e a cadeia continua no novo `audit.jsonl` (a verificação junta todos).
Para descartar o histórico antigo, arquive os `audit-*.jsonl` fora da máquina antes; a verificação
de um conjunto incompleto acusa o primeiro elo que falta, o que é o esperado.

**Limites (honestos):** a cadeia prova que ninguém alterou ou removeu registros *sem reescrever todos
os seguintes*; quem tem acesso de escrita à sua home pode reescrever a cadeia inteira. Para prova
contra isso, exporte o último `hash` periodicamente para fora da máquina (ou anexe-o a um commit).
Comandos que um comando bash dispara (um `make` que roda outros) são vistos só como o comando de topo;
arquivos que o bash muda só são vistos com os checkpoints ligados (padrão em interativo e `-p`;
desligam sozinhos acima de 20 mil arquivos ou sem git ≥ 2.25). A rede aberta por comandos do shell não
é vista aqui: isso é trabalho do sandbox (`--sandbox strict` corta a rede deles). `JAZZ_AUDIT=0`
desliga o log (só para testes: enquanto estiver desligado nada é registrado, e isso não deixa rastro).

## Rastreabilidade das mudanças

O repositório-sombra dos checkpoints (`~/.local/share/jazz-cli/checkpoints/<projeto>.git`, nunca o
`.git` do projeto) ganhou uma história em `refs/jazz/trace`: a cada passo do modelo que mudou
arquivos, ao início de cada turno e em todo fim anormal, um commit com

```
jazz checkpoint: step

Jazz-Session: 261020-214357-15e7
Jazz-Step: 2
Jazz-Model: kilo/poolside/laguna-s-2.1:free
Jazz-Prompt-Sha256: ec8eb0d624eb…
Jazz-Reason: step
```

`jazz changes <arquivo>` (alias `jazz blame`) lista, do mais novo para o mais antigo, cada commit que
mudou o arquivo com data, linhas +/−, sessão, passo, modelo e hash do pedido. Mudanças feitas à mão
entre turnos aparecem como "fora do jazz". Para ver o conteúdo:
`git --git-dir ~/.local/share/jazz-cli/checkpoints/<projeto>.git show <commit> -- <arquivo>`.
Juntando as três fontes: `jazz changes` diz quem e quando, `jazz trace <sessão>` mostra o que levou
àquele passo, `jazz audit --kind file` confirma os hashes antes/depois.
