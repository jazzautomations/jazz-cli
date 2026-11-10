# Offline no pedido do dia a dia — sonda de 89 pedidos (2026-11-09)

**O que é.** 89 pedidos escritos como um dev digita (pt informal e inglês), rodados um a um com
`jazz -p --offline --allow-tools bash,edit,write "<pedido>"` num projeto Go pequeno recriado do zero a cada
pedido (2 commits, 1 arquivo modificado, 1 não rastreado, 1 teste falhando de propósito, `.env`, CSV, JSON,
log). Binário: `main` em f89cd26. Os pedidos estão em `evals/realworld/pedidos-89.tsv`. O veredito de cada
um foi dado lendo a saída e o `git status` depois, não por verificador automático.

Não é o `jazz eval offline`: lá as tarefas foram escritas por quem escreveu as regras. Aqui ninguém olhou a
gramática antes de escrever o pedido.

## Resultado

| veredito | pedidos | % |
|---|---|---|
| fez o que foi pedido | 51 | 57% |
| recusou ou falhou num pedido fazível | 17 | 19% |
| fez outra coisa, ou respondeu errado, sem avisar | 15 | 17% |
| recusou certo (vago, ou precisa de modelo) | 6 | 7% |

Latência: 80–400 ms para quase tudo; ~1 s quando roda `go test`/`go build`.

## O achado que importa: "feito e verificado" sem ter feito

Sete pedidos terminaram com o selo `feito e verificado` sem cumprir o pedido:

| pedido | o que rodou | o que devia |
|---|---|---|
| qual foi o último commit | `git add -A` + `git commit -m 'último'` — **commitou tudo** | `git log -1` |
| roda os testes do parser | `go test -run '(?i)parser' ./...`: zero testes rodaram, "suíte passou" | os testes do pacote `parser` (um deles falha) |
| guarda minhas mudanças no stash | `git stash list` | `git stash push` |
| cria uma branch chamada teste | `git branch` (só lista) | `git branch teste` |
| formata o código | `gofmt -l .` (só lista) | formatar, ou dizer que só conferiu |
| escreve um teste pra função Soma | criou um arquivo vazio chamado `Soma` | dizer que precisa de modelo |
| explica o que o main.go faz | saída vazia (`matches (0)`, `text:`) | resumo do arquivo, ou recusa |

E mais oito respostas erradas sem selo ou com selo fraco: "quais arquivos eu mexi" e "que horas são"
devolvem a listagem de arquivos; "mostra os erros do log" lê o journal do sistema em vez de `logs/app.log`;
"quais processos tão comendo mais memória" roda `ps` dentro do sandbox e mostra só `ps`, `bash`, `bwrap`;
"mostra os últimos 3 commits" ignora o 3; "limpa o projeto" roda `go vet`; "qual o valor de PORT" olha o
ambiente e não o `.env`; "roda só o teste TestParsePrice" diz "0 passaram".

## Por que acontece

1. **O selo confere que o método rodou, não que o pedido foi atendido.** `cmd_ran` e `exit_zero` são os
   verificadores de `git_stash`, `git_branch`, `git_commit`, `format`. A invariante de honestidade cobre
   operando (slot não consumido derruba o selo); não cobre o **verbo**.
2. **Intenção de escrita com método só de leitura.** `git_stash` ("guardar mudanças") tem um único método:
   `git stash list`. `git_branch` ("listar/criar") tem um único método: `git branch`. O pedido de escrita cai
   no método de leitura e sai verde.
3. **Regra de uma palavra em intenção que muda coisa.** `git_commit` dispara com `all: ["commit"]`: qualquer
   frase com "commit", inclusive pergunta. A sobra da frase vira a mensagem.
4. **Alvo de teste vira filtro de nome.** `run_tests` com alvo usa `-run '(?i)alvo'`; se nenhum teste casa,
   `tests_passed` passa com zero testes.
5. **`create` aceita qualquer sobra como nome de arquivo.**

## Recusas em pedido fazível (17)

Typo no nome do arquivo ("abre o reamde", confiança 0.49); inglês comum ("list all go files", "what changed
since the last commit"); nome de arquivo sem caminho em scripts de dados ("quantas linhas tem o
duration.go" → `input must be a regular file`); "5 primeiras linhas do logs/app.log"; "converte
data/pedidos.csv pra json" (0.54); "onde a função Soma é usada" (0.49); "acha a palavra timeout nos logs"
(0.06); "desfaz as mudanças do main.go"; "copia o README.md pra docs" (falha: destino é pasta); "instala o
cobra" (não extrai o nome).

## A branch t5/under-refusal na mesma sonda

Mesmos 89 pedidos em d02982d: 3 melhoram ("quais arquivos mudaram hoje", "procura por TODO e FIXME",
"list all go files" deixa de recusar mas ignora o "go"), 1 piora ("quem mexeu por último no store.go" passa
a falhar). Nenhum dos sete falsos selos muda. Ela também falha 8 casos das guardas `should refuse`.

## A mesma sonda na 0.13.3

| veredito | main (f89cd26) | 0.13.3 |
|---|---|---|
| fez o que foi pedido | 51 | 55 |
| recusou ou falhou num pedido fazível | 17 | 16 |
| fez outra coisa, ou respondeu errado, sem avisar | 15 | 11 |
| recusou certo | 6 | 7 |
| selo `feito e verificado` sem cumprir o pedido | 7 | 2 |

O que mudou: a pergunta sobre o último commit não commita mais (recusa); stash e criar branch recusam em
vez de rodar a leitura; "escreve um teste" diz que precisa de modelo; "roda os testes do parser" sai como
não verificado; nome de arquivo sem caminho resolve ("quantas linhas tem o duration.go", "…pedidos.csv");
"procura por TODO e FIXME" e "quais arquivos mudaram hoje" passam a funcionar (parser T5).

Os dois selos falsos que restam: "formata o código" (só confere) e "explica o que o main.go faz" (saída
vazia). Recusar não é resolver: quatro dos pedidos que antes mentiam agora recusam um pedido que é fazível.

## O que isto diz sobre os números publicados

"Falso sucesso 0" vale para a suíte de 122 tarefas e para o corpus de 1051 pedidos. Não vale para pedido
cotidiano de dev: nesta sonda são 7 em 89. Os dois conjuntos medem o que as regras já sabem.

## Próximos passos, em ordem

1. Pergunta nunca dispara intenção que muda coisa (`qual`, `quem`, `quando`, `?` + verbo de escrita ausente).
2. Intenção de escrita sem método de escrita responde "ainda não faço isso sem modelo", nunca roda a leitura.
3. `cmd_ran`/`exit_zero` deixam de valer como verificador de método com risco ≥ med; `tests_passed` exige
   pelo menos um teste rodado.
4. Esta sonda vira suíte com veredito esperado por pedido e entra no CI ao lado do eval.
5. Só então cobertura: resolver nome de arquivo sem caminho, typo em nome de arquivo, inglês básico.

## A sonda virou suíte, e três conjuntos inéditos mediram de verdade (2026-11-10)

`jazz eval real` roda os 89 pedidos com veredito por pedido: texto esperado, estado do repositório depois
(`after`) e, para todo pedido que não declara `changes`, árvore, HEAD, branches e stash idênticos. Passa
pela mesma porta do `jazz -p --offline --allow-tools bash,edit,write` (atalhos primeiro, cérebro depois) e
só deixa rodar comando que fica dentro do projeto de teste. `jazz eval parse "pedido"` mostra a leitura sem
agir. `--tasks ARQ` roda um conjunto externo.

Consertar olhando os 89 pedidos infla o número desses 89. Por isso cada rodada foi medida num conjunto
**inédito**, escrito por um agente que não viu a gramática nem o código, só a descrição do projeto de teste
e a lista de pedidos já usados. Os três estão em `evals/realworld/heldout-{1,2,3}.json`.

| conjunto | primeira passada (o que mede) | depois de consertar olhando as falhas |
|---|---|---|
| 89 da sonda (já vistos) | 59 · 10 falsos sucessos | 88 · 0 |
| inédito 1 (100) | 61 · 11 falsos sucessos | 100 · 0 |
| inédito 2 (120, mais difícil: comando cru, pedido embrulhado, 13 typos) | 49 · 13 falsos sucessos | 64 · 0 |
| inédito 3 (100, distribuição do dia a dia) | **47 · 7 falsos sucessos** | não consertado: é o número atual |

Leitura honesta: em pedido escrito por outra pessoa, o offline acerta cerca de metade na primeira vez. A
cobertura em frase nova **não subiu** de uma rodada para a outra (61 → 49 → 47; os conjuntos não têm a
mesma dificuldade, mas a tendência não é de alta). O que caiu foi o falso sucesso, e ainda não chegou a
zero em conjunto novo. Os números da coluna da direita são de desenvolvimento, não de medida.

### O que os conjuntos inéditos acharam (tudo presente na 0.13.3)

- "limpa o projeto" era lido como `container_prune` e rodava `docker image prune -af`: a regra era a
  palavra "limpa" sozinha.
- `cmd_ran` selava "feito e verificado" um comando de escrita com exit ≠ 0.
- Escrita negada pela permissão caía no método de leitura irmão e saía verde (`git stash push` negado →
  `git stash list`). É o ticket T1 de `jev-build-time-tickets.md` na forma estreita.
- Pergunta sobre commit commitava por três portas diferentes: dentro de pedido composto ("…e me diz qual
  foi o último commit"), com interjeição ("uai, qual era a mensagem do commit…"), e "the last commit" como
  segundo passo. A regra de `git_commit` é a palavra "commit".
- Arquivo citado e ignorado: "any IP addresses in logs/app.log?" respondia com as interfaces da máquina.
- "procura localhost no projeto todo" virava a lista de TODOs.
- "preciso de uma pasta nova chamada build/artefatos" criava um arquivo chamado `nova`.
- "formata só o store.go, o resto deixa quieto" formatava tudo.
- "ultmas 2 linhas" mostrava as duas primeiras.
- `ps` dentro do sandbox listava só `ps`, `bash` e `bwrap`.

### O que entrou

Guardas (recusam em vez de errar): pergunta com interjeição ou embrulho; `git_commit` exige verbo de
commit; arquivo do projeto citado e não usado pela leitura; pedido sobre git respondido por leitura que
não é de git; "todo" como palavra; nome de arquivo que não é nome; pedido composto feito pela metade;
verbo criativo no começo do pedido; método de leitura nunca é fallback de método de escrita; `cmd_ran` de
método que muda coisa exige exit 0.

Cobertura: frases feitas (`nlu_phrases.go`) com operando resolvido no projeto e correção de digitação só
nas palavras da frase; atalhos de leitura novos em `intents.json`; passo de pedido composto passa pelos
atalhos; comando digitado cru roda como veio (sandbox e permissão); releitura sem o embrulho ("dá pra…",
"preciso ver…"); stash, criar branch e descartar mudanças de um arquivo com pós-condição conferida;
formatar reescreve e confere; soma/média/maior/menor/contagem de CSV; chaves e valores de `.env` com
credencial oculta; erros de arquivo de log; resumo estrutural de arquivo; alvo de teste por pacote.

### O que isto diz sobre o caminho

Frase feita por pedido não escala: três rodadas, cobertura em frase nova parada em torno da metade. As
guardas escalam melhor (o falso sucesso caiu). Os próximos passos que os estudos já apontavam e que esta
medição confirma: gate estático de assets e matriz slot↔método (`offline-optimization-study.md` #2 e #6,
que teriam pego `git_stash` só com método de leitura e `cmd_ran` em risco alto), corpus metamórfico (#8),
convergência dos dois parsers (#4), e recusa com causa nomeada (#10) para o pedido que hoje cai no menu.
