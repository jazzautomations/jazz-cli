# O que o modelo vê do histórico — replay de 40 decisões (2026-11-11)

**O que é.** `jazz eval context` refaz decisões de sessões gravadas: o mesmo modelo recebe o histórico como
estava e o histórico sob uma política de contexto (`ctxview.go`), e as duas próximas ações são comparadas. O
histórico inteiro é pedido duas vezes, porque o modelo não concorda consigo mesmo sempre: essa taxa é o teto
contra o qual a política é julgada.

O critério vem de *Tools Need Not Speak in Text* (arXiv 2611.06500): uma observação comprimida é boa quando
preserva a próxima ação, não quando preserva o texto. O método do artigo (tokens latentes injetados no
embedding) exige os pesos do modelo e não serve a um agente que fala com provedores por API; o critério serve.

## De onde vem o contexto

Medido em 448 sessões com chamada de modelo (4.801 chamadas), em bytes de entrada acumulada:

| fatia | % |
|---|---|
| saída de ferramenta | 50,5 |
| falas do agente (texto e argumentos de chamada) | 28,0 |
| prompt de sistema | 11,9 |
| pedidos do usuário | 9,6 |

44,6% da entrada é saída de ferramenta fora das três mais recentes. 4% dos resultados passam de 8 KB e
carregam 46,5% dos bytes de ferramenta. Só 15 das 61 sessões com 15 ou mais chamadas de ferramenta chegaram
a compactar: as outras reenviaram tudo até o fim.

O contexto velho é sobretudo **leitura de arquivo**: sem tocar em resultado de `read`, o corte da política
mais simples cai de 70% para 8% nas decisões afetadas.

## As três políticas

Em todas, as falas do agente e os três resultados mais recentes ficam literais, e resultado de até 1 KB
nunca é tocado. O que muda é o que vira um aviso de uma linha (tamanho, primeira linha, caminho do arquivo
de spill quando há):

- `tier`: todo resultado antigo e grande.
- `stale`: como `tier`, mas uma leitura só vira aviso quando o mesmo arquivo foi lido de novo depois.
- `edited`: como `stale`, mais a leitura de um arquivo editado ou regravado depois.

## Resultado

40 decisões de sessões de avaliação (no máximo 3 por sessão), `kilo/cohere/north-mini-code:free`,
temperatura 0. "Mesmo alvo" = mesma ferramenta e mesmo arquivo, padrão ou começo de comando.

| | mesma ferramenta | mesmo alvo | mesmos argumentos | tokens de entrada |
|---|---|---|---|---|
| inteiro × inteiro (teto) | 39/40 | 36/40 | 31/40 | — |
| `tier` | 36/40 | 30/40 | 21/40 | −57,8% |
| `stale` | 39/40 | 34/40 | 27/40 | −3,5% |
| `edited` | 38/40 | 33/40 | 24/40 | −12,4% |

As divergências de alvo, por causa:

| | total | o modelo também divergiu de si | releu um arquivo | fez outra coisa |
|---|---|---|---|---|
| `tier` | 10 | 1 | 5 | 4 |
| `stale` | 6 | 4 | 1 | 1 |
| `edited` | 7 | 4 | 1 | 2 |

`stale` e `edited` ficam no teto, descontado o ruído do modelo, e cortam pouco. `tier` corta mais da metade
dos tokens e paga com releituras (um passo a mais, recuperável) e com quatro ações diferentes. Uma delas é
dano: num laço sobre arquivos numerados o modelo perdeu a conta e pulou do 04 para o 10 — o aviso apagou o
registro de onde ele estava.

## O que isto não mede

- Concordância de próxima ação é por decisão. Não diz se a tarefa termina certa, nem quantas releituras
  uma tarefa inteira acumula. Falta a bateria ponta a ponta com a visão ligada e desligada.
- Um modelo só, fraco e grátis. O teto de 36/40 no alvo é dele.
- Cache de prompt: `stale` e `edited` reescrevem uma mensagem antiga quando o arquivo é relido ou editado,
  o que invalida o prefixo dali em diante naquela chamada.

## Estado

`context_view` (`JAZZ_CONTEXT_VIEW`) = `off | tier | stale | edited`, **off por padrão**. A visão é uma
função pura aplicada ao montar o pedido; o histórico guardado não muda. A estimativa de tokens segue a
visão quando o provedor ainda não devolveu contagem.

Próximos: a bateria ponta a ponta; um registro de estado mantido passo a passo (arquivos tocados, itens
já feitos), que é o que falta para `tier` não perder a conta; e avisos que guardem também a última linha
do resultado.

```
jazz eval context --dry                      # só o corte, sem chamar modelo
jazz eval context --compare --n 40           # as três políticas nos mesmos pontos
jazz eval context --compare --resume r.json  # continua quando a cota do provedor acabou no meio
```
