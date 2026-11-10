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
