# Política de decisão sob posterior calibrado: agir, perguntar, menu

Pesquisa de 04/11/2026. Continua [unifying-math-offline.md](unifying-math-offline.md) (a tese: **posterior calibrado, não
entropia**; calibração e entropia são ortogonais; `calib.go` desligado é o buraco mais caro) e
[math-offline-brain.md](math-offline-brain.md) (P1–P4: ECE/Brier, Platt/isotônico, Chow, risco–cobertura). O foco aqui
é o elo que falta entre a probabilidade e a ação: **qual função de decisão transforma `p` calibrado em agir,
perguntar, menu, entregar ao modelo ou recusar**, com custo assimétrico (`false_success` caríssimo, perguntar barato) e
**dados escassos** (75 tarefas, ~644 exemplos, 50 intenções).

Este documento não edita código. Ele mapeia o que existe (`file:line`), propõe a política em Go para o Dell
(i686, 1 CPU, 2 GB), e diz com todas as letras onde a resposta boa não existe.

---

## 0. Resumo executivo

**Tese.** A decisão do cérebro offline é uma decisão Bayesiana com opção de rejeição sob custo:

```
a* = argmin_{a ∈ A(x)} E[ C(a, Y) | x ]
```

com `A(x) = {agir, perguntar, menu, entregar_ao_modelo, recusar}`. A variável de decisão é a **probabilidade
calibrada de sucesso da ação**, `P(sucesso | x, a)`, não a entropia, não o score cru, não a confiança que o
classificador inventou. A regra ótima para agir vs. rejeitar é a de Chow (1970): age-se quando
`(1 − p) · C_erro ≤ C_rejeitar`. Com custo assimétrico por banda de risco, isso vira
`p ≥ 1 − C_alternativa / C_erro(banda)`. O menu é uma rejeição com espaço de resposta restrito; a pergunta é uma
rejeição com espaço livre; o handoff é uma ação com custo e confiabilidade próprios; a recusa é a ação "nenhuma das
outras". Todos caem na mesma desigualdade.

**O que muda na prática.**

1. Quatro limiares hoje desconexos (`nuMinConf=0.6`, `brLevel0Min=0.85`, a margem de `brPick`, o empate de `niTop`)
   viram **uma tabela de τ por banda de risco**, derivada do custo, não escolhida no olho.
2. `calib.go` (ECE/Brier/Platt/isotônico) sai do limbo: um asset `nlu/calib.json` por canal, ajustado no forge
   (amd64), consumido no Dell em O(1), sem alocação.
3. `mhMethod.Risk` e `Capability.Risk` — hoje **metadados mortos** (ninguém lê; `cbParseRisk` não tem chamador) —
   passam a definir a banda. Sem isso, a política não tem como saber que apagar arquivo e abrir README não são a
   mesma ação.
4. O alvo da calibração deixa de ser `P(intenção certa)` e passa a ser `P(sucesso da ação)`, com a ressalva de que
   não se multiplica probabilidade de estágios sem checar correlação; usa-se a cota **inferior** de
   Fréchet–Hoeffding, `max(0, p_intent + p_slot − 1)`, ou uma calibração ponta-a-ponta. O `min(...)` é a cota
   superior, não um minorante: usá-lo para agir é otimista.
5. A validação deixa de ser "rodar o eval e ver 75/75": vira **LOO/k-fold agrupado + Clopper–Pearson + bootstrap por
   grupo + faixa de guarda + gate**, e o eval de 75 tarefas vira teste de fumaça, não certificado.

**A política em cinco linhas.**

1. `band = bandOf(método)` — read, reversible, external ou irreversible, derivada de `Effects`/`Undo`/`Verify` e do
   `risk` declarado como piso.
2. `p = calibrado(P(sucesso))` por canal; se há resolvedor, `p = max(0, p_intent + p_slot − 1)`.
3. Se `band == BandIrreversible`, **perguntar/recusar sempre**, independentemente de `p`.
4. Se `p ≥ τ(band, modo)`, **agir**.
5. Senão: **menu** se os candidatos cobrem a massa (`TopMass ≥ 0.90`, `k ≤ 5`); **pergunta** se há slot obrigatório
   vazio; **handoff** se online; **recusa** se offline headless.

**O que não muda.** Não se usa LLM no caminho crítico de runtime. O ajuste é compile-time (forge); o runtime só
aplica uma tabela e um mapa pequenos. Nada aqui exige embeddings, rede ou dependência externa.

**A parte honesta.** 75 tarefas com 0 `false_success` dão um limite superior de 95% de ~4% (regra de três): o eval
**não certifica** 1% de `false_success`. Os 644 exemplos são curados, não uma amostra aleatória dos pedidos reais;
a calibração ajustada neles é uma **priori**, não uma medição. O custo real de interromper o usuário é uma decisão de
produto, não um número que se estima de 75 tarefas. O resto deste documento é o que dá para fazer com essa
honestidade.

---

## 1. A tese e o problema

### 1.1 O núcleo: decisão com rejeição sob custo

Seja `x` o pedido, `Y` a ação correta (intenção + slots + método), `p_i = P(Y = i | x)` a posterior calibrada sobre as
ações candidatas, e `C(a, y)` a matriz de custo. A decisão é a de teoria da decisão bayesiana
([Berger, *Statistical Decision Theory and Bayesian Analysis*, 2ª ed., Springer 1985](https://doi.org/10.1007/978-1-4757-4286-2)).
A regra de Chow para o par agir/rejeitar com custo de rejeição `c`:

```
E[C(agir em i)] = (1 − p_i) · C_erro(i)
E[C(rejeitar)]  = c
agir em i  ⇔  (1 − p_i) · C_erro(i) ≤ c
           ⇔  p_i ≥ 1 − c / C_erro(i)
```

A prova é pontual e não usa hipótese nenhuma além da matriz de custo: para cada `x`, escolhe-se a ação de menor custo
esperado; minimizar ponto a ponto minimiza a esperança global. É o resultado de
[Chow, *On optimum recognition error and reject tradeoff*, IEEE Trans. Inf. Theory 16(1):41–46, 1970](https://doi.org/10.1109/TIT.1970.1054406).
Para o caso multiclasse com custos iguais, a regra colapsa em `max_i p_i ≥ 1 − c`; com custos diferentes por ação,
o conjunto aceito é `{i : (1 − p_i) C_erro(i) ≤ c}`, que **não é** simplesmente um limiar no máximo. Essa distinção é
o coração deste documento.

**Por que a matriz de custo do jazz é assimétrica.** `false_success` (dizer "sucedeu" quando não sucedeu) é o evento
que o eval trata como bloqueante (`evaloffline.go:399-407`), e é o que corrói confiança. Perguntar custa um turno;
agir errado num `rm` custa o arquivo, num `git push` custa o remoto, num `systemctl restart` custa o serviço. A
assimetria não é um detalhe: ela é o motivo de existir a política. A regra de Chow com `c` pequeno e `C_erro` grande
produz `τ` perto de 1; com `c` grande e `C_erro` pequeno, `τ` perto de 0.5.

**Calibrar e depois limiar, não treinar cost-sensitive.** Com probabilidades calibradas, a decisão de menor custo
esperado é um limiar; não é preciso treinar um classificador cost-sensitive separado
([Elkan, *The Foundations of Cost-Sensitive Learning*, IJCAI 2001](https://ijcai.org/proceedings/2001/133)). É por
isso que a calibração vem antes da política: ela transforma o score em uma probabilidade à qual o limiar de custo se
aplica. Sem calibração, o limiar é um número sem unidade.

**Custo de rejeição não é um número só.** Rejeitar pode significar perguntar (`c_ask`), oferecer menu
(`c_menu`), entregar ao modelo (`c_model`) ou recusar (`c_refuse`). A política escolhe a **alternativa mais barata**
que ainda resolve o problema:

```
agir  ⇔  (1 − p) · C_erro(banda) ≤ min(c_ask, c_menu, c_model, c_refuse)
```

Se `p` não passa, a ação é a alternativa de menor custo esperado, não necessariamente perguntar. Em headless,
`c_ask = c_menu = ∞` (ninguém para responder), então a escolha é handoff ou recusa. Em offline headless,
`c_model = ∞`, então é recusa. Isso já está, em parte, no código (`env.Ask == nil`), mas não está formalizado nem
ligado ao custo.

### 1.2 Por que entropia não é a variável de decisão

A tentação é dizer "quando a entropia da distribuição de intenções é alta, pergunta". Para duas classes, a entropia
é monótona no máximo posterior no ramo relevante, então as duas regras coincidem. Para 50 intenções, não coincidem.
Contraexemplo concreto, três classes:

```
p = (0.50, 0.25, 0.25)   H(p) = 1.500 bits   max p = 0.50
q = (0.40, 0.40, 0.20)   H(q) = 1.522 bits   max q = 0.40
```

`p` tem **maior** máximo posterior e **menor** entropia. Uma regra de Chow com `τ = 0.45` aceita `p` (0.50 ≥ 0.45) e
rejeita `q` (0.40 < 0.45). Uma regra de entropia com `τ_H = 1.51` rejeita `p` (1.500 < 1.51) e aceita `q`
(1.522 ≥ 1.51). As duas regras **discordam**. A regra de Chow é a ótima para o custo 0–1–`c`; a de entropia não é.
Isso já está provado em [unifying-math-offline.md §3.1](unifying-math-offline.md); o contraexemplo acima é a versão
numérica que faltava.

O que a entropia carrega de verdade é outro problema: **quantas perguntas binárias faltam** para identificar `Y`
(twenty questions). Para escolher *qual* pergunta fazer, a entropia (ou a informação mútua esperada) é a moeda certa:
`H(Y) ≤ E[n] < H(Y) + 1` para uma árvore binária balanceada
([Cover & Thomas, *Elements of Information Theory*, 2ª ed., Wiley 2006](https://doi.org/10.1002/047174882X)). Mas a
decisão *agir vs. perguntar* é Chow, não entropia. São dois usos diferentes; misturá-los produz a regra errada em
multiclasse.

### 1.3 Calibração e entropia são ortogonais — prova

Fixe 100 pedidos e o mesmo vetor `p = (0.9, 0.1)` para todos. A entropia é `H = 0.469` bits, constante. Se 90
acertam, o modelo é calibrado; se 60 acertam, não é. **Mesma entropia, calibração diferente.** Reciprocamente, um
modelo calibrado pode emitir `p = (1, 0)` em pedidos fáceis (`H = 0`) e `p = (0.5, 0.5)` em pedidos ambíguos
(`H = 1`), mantendo `P(acerto | p) = max p` em cada nível. **Calibração perfeita, entropias diferentes.** Logo,
nenhuma implica a outra. A calibração top-label é, ela própria, incompleta: dois inputs com o mesmo score podem ter
taxas de acerto diferentes — é o **grouping loss**
([Perez-Lebel, Le Morvan & Varoquaux, *Beyond calibration: estimating the grouping loss of modern neural networks*,
ICLR 2023, arXiv:2210.16315](https://arxiv.org/abs/2210.16315)). Para decidir, você precisa da calibração **e** de
features de contexto que separem os grupos. O §5 deste documento é sobre quais features.

**Consequência para o jazz.** `Intent.Confidence` é uma função heurística de constantes (0.9, 0.97, 0.75·p,
0.55+0.45·p, caps 0.5 e ×0.6 — `nlu.go:953-1067`), não uma probabilidade. `calib.go` tem a matemática para medir e
corrigir (`ECE` em `calib.go:164`, `BrierScore` em `:198`, `PlattFit` em `:220`, `PlattApply` em `:265`,
`IsotonicFit` em `:281`, `IsotonicApply` em `:326`), mas **nenhum caminho de produção a chama** — só
`calib_test.go`. O mapa monotônico não muda o argmax; muda a decisão de agir. É exatamente por isso que a peça
desligada é cara: sem ela, todo limiar é um palpite.

### 1.4 O alvo da calibração: `P(intenção)` não é `P(sucesso)`

O código chama de `Confidence` a confiança na **intenção**. A decisão precisa da probabilidade de **sucesso da
ação**, que depende de pelo menos três estágios:

```
p_sucesso = P(intenção certa, slots certos, método aplicável e executado com sucesso | x)
```

Idealmente o método tem um verificador que confirma o sucesso; sem verificador, "executado" não é "sucedido", e a
política deve tratar a ausência como uma banda acima (§3.6).

Com 644 exemplos e 50 intenções, estimar `p_sucesso` por intenção é esparso (12,9 exemplos/intenção). O caminho
honesto é:

1. calibrar `p_intent` por canal (é o estágio dominante e o mais barato de medir);
2. tratar o resolvedor de nomes como **outro canal**, com mapa próprio (as features são outras: `Dist`, `Exact`,
   margem, extensão, hint, recência);
3. tratar a aplicabilidade do método como **fato estrutural** (as `pre` já dizem se o método roda; o `Verify` diz se
   o resultado é verificável), não como probabilidade;
4. para a decisão, usar a **cota inferior** de Fréchet–Hoeffding:
   `max(0, p_intent + p_slot − 1) ≤ P(ambos) ≤ min(p_intent, p_slot)`. O `min(...)` é a cota **superior**, não um
   minorante; usá-lo para agir é otimista. O produto `p1·p2` só é válido sob independência, que o jazz não tem: uma
   intenção errada costuma arrastar o slot errado. Para decidir com segurança, use a cota inferior (conservadora) ou
   calibre o fim-a-fim. A referência das cotas é
   [Nelsen, *An Introduction to Copulas*, 2ª ed., Springer 2006](https://doi.org/10.1007/0-387-28678-0).

Isso é uma correção conceitual importante: hoje `brLevel0Min` olha só `in.Confidence` (`brain.go:610`), então um
pedido com intenção clara mas alvo ambíguo ("abre o config", com três `config.*`) pode passar no gate com
`p_intent = 0.97` e falhar no resolvedor. O `brPick` pega parte disso nas intenções que usam `resolve_name` +
`choose` (`open_file`, `explain`, `tool_info`, `tool_status`, `connect_host`), mas não em `delete_file`, `rename` e
`create`, que vão direto ao `file_op` com o slot cru (§3.7). E o `brPick` decide com uma regra de distância de
edição, não com uma probabilidade. A política precisa das duas coisas.

---

## 2. O que o código faz hoje (mapeamento `file:line`)

### 2.1 Quatro limiares desconexos

| Limiar | Onde | O que mede | Como foi escolhido |
|---|---|---|---|
| `nuMinConf = 0.6` | `nlu.go:45`, usado em `Understood()` `nlu.go:61-64` | "entendi o pedido" | constante, nunca medida |
| `brLevel0Min = 0.85` | `brain.go:596`, usado em `brain.go:610` e `:655` | "posso responder sem modelo" | constante, nunca medida |
| `brPick` | `methods.go:1132-1155` | "posso agir num candidato sem perguntar" | regras: exato, único seguro, líder com margem `fzSlip*8 = 16` |
| `niTop` | `nameindex.go:710-718` | "quantos candidatos estão empatados" | empate se `Score − top < fzSlip = 2` |

Os quatro são decisões do mesmo tipo (agir vs. rejeitar), mas usam escalas diferentes: probabilidade, confiança
heurística e distância de edição. Não há uma calibração compartilhada, nem uma tabela de custo. O `brPick` é
explicitamente uma regra de decisão em unidades de score, não em probabilidade (`methods.go:1150`: "one far
candidate: confirm, never assume"). O `niTop` usa `fzSlip` como margem de empate, que é um parâmetro de distância,
não de risco.

### 2.2 `calib.go` está morto

`ECE`, `MCE`, `BrierScore`, `PlattFit`, `PlattApply`, `IsotonicFit`, `IsotonicApply` não são chamados por nenhum
caminho de produção. A verificação é `rg` no repo: as únicas referências fora de `calib.go` são `calib_test.go`.
Isso significa que:

- não se sabe o ECE de nenhum canal;
- não se sabe se `nuMinConf=0.6` corresponde a 60% de acerto;
- não se sabe se `brLevel0Min=0.85` corresponde a 85%;
- a "confiança" que a UI mostra (`brain.go:216`: "talvez X, confiança 0.50") não é uma probabilidade.

O código está correto e testado (`calib_test.go`), mas é pesquisa não integrada. O buraco não é a matemática; é o
chamador e o asset.

### 2.3 `mhMethod.Risk` e `Capability.Risk` são metadados mortos

- `mhMethod.Risk string` está em `methods.go:70`; os 49 arquivos `methods/*.json` declaram `risk` (low/med/high).
  **Nenhum caminho de produção lê o campo.** A varredura `rg '\.Risk'` só encontra atribuições em `caps.go`.
- `Capability.Risk cbRisk` está em `caps.go:72`; `cbParseRisk` (`caps.go:47`) **não tem chamador**; o campo é
  atribuído em `cbRegistry` (`caps.go:133-137`) para scripts, mas nunca consultado por uma decisão.
- A permissão real é decidida por `Permit` (`methods.go:265`, `:551`, `:685`, `:925`), que classifica a **linha de
  comando**, não a capacidade. O `Risk` da capacidade e o `risk` do método não entram nessa conta.

Consequência: a política não tem como distinguir `open_file` de `delete_file` pelo metadado; ela teria que olhar o
nome do método ou o comando. O `risk` declarado é a informação certa, mas está desconectada. P1 deste documento
liga.

### 2.4 O que o eval mede

`evaloffline.go:91-101` define o resumo: `Success`, `FalseSuccess`, `BlockOK`, `FalseBlocks`, `NoLLM`,
`NoLLMRate`, `QuestionsAsked`, `QuestionsNeeded`, `CmdsPerTask`, `P50MS`, `P95MS`, `PeakRSSKB`. O `false_success`
é calculado em `evaloffline.go:399-407`:

```go
if r.Status == tkSucceeded && !row.Success { row.FalseSuccess = true }
if exp.Handled != nil && !*exp.Handled && r.Handled && r.Status == tkSucceeded {
    row.FalseSuccess = true
}
```

Duas coisas importantes:

1. O primeiro caso é "disse sucedido e a resposta está errada" — o custo de confiança.
2. O segundo é "agiu quando o eval esperava handoff" — **uma expectativa de política**, não um erro factual. Se o
   cérebro resolveu corretamente uma tarefa que o eval marcou como `handled:false`, isso conta como
   `false_success`. Isso significa que calibrar contra o `false_success` do eval pode aprender a **política do
   eval**, não a do usuário. Use o eval como teste de regressão, não como rótulo de treino.

O `false_blocks` (`eoSummary.FalseBlocks`) é o custo oposto: uma tarefa que devia ser resolvida foi bloqueada. O
eval trata `FalseSuccess > 0 || FalseBlocks > 0` como falha do relatório (`evaloffline.go:164`), mas os dois não são
simétricos: `false_success` é segurança; `false_blocks` é usabilidade. A política deve ser lexicográfica:
primeiro `false_success = 0`, depois minimizar `false_blocks` e maximizar `no_llm_rate`.

### 2.5 Onde a decisão realmente acontece

| Ponto | Onde | Comportamento hoje |
|---|---|---|
| `brRun` → `brRunIntent` | `brain.go:41-70`, `:126-183` | `!Understood()` → typo confirm ou `brNotUnderstood` |
| Typo confirm | `brain.go:126-148` | pergunta "Rodar agora / Mandar pro modelo"; se confirma, seta `Confidence = 0.6` (hack) |
| `brNotUnderstood` | `brain.go:214-224` | recusa honesta com lista de capacidades |
| Nível 0 headless | `brain.go:604-647` | `Confidence ≥ 0.85` + intenção read-only; senão devolve ao modelo |
| Nível 0 interativo | `brain.go:649-699` | mesmo gate, mas imprime e anota |
| `brInteractive` | `brain.go:701-733` | tenta o cérebro; se não handled, `offline.go` oferece menu |
| Menu de ofertas | `offline.go:379-395`, `offers.go:158-330` | ações determinísticas, não uma pergunta com candidatos |
| `brPick` | `methods.go:1132-1155` | exato / único seguro / líder com margem |
| `brChoose` | `methods.go:1157-1220` | se `brPick` falha, menu de até 5; se `Ask == nil`, `tkNeedsInput` |
| `brAskSlot` | `methods.go:1222-1240` | pergunta livre; se `AskText == nil`, `tkNeedsInput` |
| `brExecute` | `brain.go:261-420` | métodos por prioridade; `pre` e `needs`; verificação no fim |
| `brVerify` | `methods.go:1680-1760` | verificadores; `tkUnverified` se não dá para verificar |

O padrão é uma cascata de regras: entendeu? tem método? as precondições valem? o passo rodou? o verificador passou?
Cada "não" tem uma reação hard-coded. Não há uma função de custo, nem uma estimativa de `P(sucesso)`, nem uma
tabela de bandas. O `risk` do método está lá, mas não é lido.

**O hack do typo.** `brain.go:143` faz `in.Confidence = nuMinConf` depois que o usuário confirma. Isso transforma
uma confirmação binária (alta confiabilidade) em uma probabilidade de 0.6 (baixa). Funciona porque a chamada
recursiva só checa `Understood()` (0.6 ≥ 0.6), mas é semanticamente errado: a confirmação do usuário é uma
observação nova, não um bump de 0.6. O certo é um campo `Confirmed bool` e uma regra "confirmado dispensa o limiar
de intenção, mas não o de banda/verificador". P1 pode incluir isso; é uma linha.

**O menu em headless.** `brChoose` e `brAskSlot`, sem callback, retornam `tkNeedsInput` com o texto da pergunta
(`methods.go:1205`, `:1235`). Em headless, ninguém responde; o `brHeadlessLevel0` trata `tkNeedsInput` como
"não handled" e devolve ao modelo (`brain.go:623-627`). Offline headless, o resultado é a recusa com o texto da
pergunta. Isso é um `DecRefuse` disfarçado de pergunta. A política deve marcar `CanAsk=false` e escolher
explicitamente `DecHandoff`/`DecRefuse`, não imprimir um menu que ninguém pode responder.

---

## 3. A política concreta

### 3.1 Bandas de risco derivadas dos metadados

O `risk` declarado (`low/med/high`) é grosseiro: `run_tests` é `med`, mas rodar testes é muito mais seguro que
`install_dep` (`high`); `delete_file` é `high`, mas o checkpoint cobre o `/undo`; `git_push` é `high` e não tem
undo. A banda deve ser derivada de **quatro eixos**, não de uma string:

| Eixo | Pergunta | Fonte no código |
|---|---|---|
| **Efeito** | read/fact vs. write vs. exec/net vs. git | `Capability.Effects` (`caps.go:74`), `mhStep.Cap` |
| **Reversibilidade** | dá para desfazer? | `Capability.Undo` (`caps.go:78`), `AfterWrite`/checkpoint (`brain.go:265`) |
| **Verificabilidade** | um verificador pega o erro antes do claim? | `mhMethod.Verify` (`methods.go:67`), `Capability.Verifier` (`caps.go:76`) |
| **Observabilidade** | o usuário vê o resultado e corrige? | read-only devolve texto; write devolve diff; exec é opaco |

Proposta de banda:

```go
type RiskBand uint8

const (
    BandRead         RiskBand = iota // sem mudança de estado, verificável/observável
    BandReversible                   // escrita com Undo=checkpoint, ou exec read-only verificado
    BandExternal                     // exec/net com efeito fora do repo, sem undo confiável
    BandIrreversible                 // sem undo, sistema/remoto/destrutivo
)
```

Derivação:

```go
func capBand(c Capability) RiskBand {
    switch {
    case c.Risk == cbCritical:
        return BandIrreversible
    case c.DryRun && onlyReadEffects(c.Effects):
        return BandRead
    case c.Undo != "" && c.Risk <= cbMed:
        return BandReversible
    case c.Risk >= cbHigh:
        return BandExternal
    }
    return BandReversible
}

func capBandStep(st mhStep) RiskBand {
    c, ok := brCapMeta[st.Cap]
    if !ok {
        return BandExternal // capacidade desconhecida: conservador
    }
    switch st.Cap {
    case "git_op":
        if st.In["sub"] == "push" || st.In["sub"] == "force" {
            return BandIrreversible
        }
        if st.In["sub"] == "status" || st.In["sub"] == "diff" || st.In["sub"] == "log" {
            return BandRead
        }
    case "file_op":
        if st.In["op"] == "delete" || st.In["op"] == "rename" {
            return BandReversible // o checkpoint cobre o /undo
        }
    }
    return capBand(c)
}

func bandOf(m mhMethod) RiskBand {
    b := BandRead
    for _, st := range m.Steps {
        if cb := capBandStep(st); cb > b { b = cb }
    }
    if d := RiskBand(cbParseRisk(m.Risk)); d > b { b = d } // declarado é piso, não teto
    if len(m.Verify) == 0 && b < BandExternal { b++ }      // não verificado sobe uma banda
    return b
}
```

**Problema real:** `brCapMeta` não existe. `cbRegistry` (`caps.go:110-149`) lista `cbBrainCaps` e `cbToolCaps`,
mas **não** lista `file_op`, `git_op`, `pkg_cmd`, `systemd`, `container_op`, `proc_kill`, `undo`, `proj_cmd`,
`read_cmd`, `ask_slot`, `say`, `tool_report`, `tool_status`, `host_report`, `host_list`, `net_status`,
`test_context` — todas em `brCaps` (`methods.go:302-329`). O registry e o executor são mapas disjuntos para as
capacidades que mais importam (escrita/exec). Sem uma tabela de metadados para elas, a banda não fecha. P1 inclui
essa tabela; é a peça que transforma o `risk` declarado em mecanismo.

**Dispatchers.** `git_op`, `file_op`, `pkg_cmd`, `systemd`, `container_op` e `proc_kill` são dispatchers: a banda
depende do `in["sub"]`/`in["op"]`, não só do nome da capacidade. `git status` é read; `git push` é irreversível no
remoto; `file_op delete` é reversível pelo checkpoint; `pkg_cmd install` é external/irreversível; `systemd restart`
é external. `bandOf` precisa olhar o passo (`mhStep.In`), não só `st.Cap`; sem isso, `git_push` cai em
`BandExternal` e a regra dura do irreversível não dispara. É um detalhe de implementação com consequência de
segurança; o P9 (teste de consistência) deve cobrir os dispatchers.

**Regra dura:** `BandIrreversible` **nunca** age automaticamente, independentemente de `p`. Se há usuário,
pergunta com confirmação explícita; se não há, recusa. Isso não é um `τ` alto; é uma regra separada, porque o custo
de um `rm -rf` errado não é "mais um erro", é uma perda irreparável. A regra de Chow com `C_erro → ∞` já diz isso
(`τ → 1`), mas a implementação precisa do caso especial para não depender de `p` chegar a 1 (o classificador capa em
0.97; ver `nlu.go:976`).

### 3.2 A função de decisão

```go
type Decision uint8

const (
    DecAct Decision = iota
    DecAsk
    DecMenu
    DecHandoff
    DecRefuse
)

type ActionEnv struct {
    CanAsk   bool // há UI interativa (env.Ask/AskText != nil)
    CanModel bool // online e com modelo disponível
}

type QuestionCtx struct {
    HasQuestion   bool    // existe um slot obrigatório vazio que a pergunta preenche
    HasCandidates bool    // existe um conjunto de candidatos do resolvedor
    K             int     // tamanho do menu (≤ 5)
    TopMass       float64 // massa de probabilidade coberta pelos k candidatos
}

type Policy struct {
    Tau      [4]float64 // τ por banda: age se p ≥ Tau[banda]
    TauAsk   [4]float64 // abaixo disso, nem sabe o que perguntar
    MenuMass float64    // massa mínima para oferecer menu
    MenuMax  int
}

func (pol Policy) Decide(p float64, band RiskBand, env ActionEnv, q QuestionCtx) (Decision, string) {
    if band == BandIrreversible {
        if env.CanAsk {
            return DecAsk, "irreversível: confirmação humana obrigatória"
        }
        return DecRefuse, "irreversível sem usuário"
    }
    if p >= pol.Tau[band] {
        return DecAct, "p acima do limiar da banda"
    }
    if env.CanAsk {
        if q.HasCandidates && q.K <= pol.MenuMax && q.TopMass >= pol.MenuMass {
            return DecMenu, "menu cobre a massa de candidatos"
        }
        if q.HasQuestion && p >= pol.TauAsk[band] {
            return DecAsk, "pergunta discriminante disponível"
        }
    }
    if env.CanModel {
        return DecHandoff, "modelo disponível"
    }
    return DecRefuse, "sem alternativa"
}
```

A ordem importa:

1. **Irreversível** vence tudo. Não é uma questão de `p`.
2. **Agir** se `p` passa o limiar da banda.
3. **Menu** se os candidatos cobrem a massa e cabem em 5. O menu é preferível à pergunta livre quando o espaço de
   resposta é conhecido: o usuário gasta menos (escolhe um número) e a resposta é interpretável.
4. **Perguntar** se há um slot obrigatório vazio e `p` ainda é alto o suficiente para a pergunta fazer sentido.
5. **Handoff** se há modelo; **recusar** se não há.

`TauAsk` é um guarda, não um teorema: se `p` é muito baixo, o sistema pode não saber nem qual pergunta fazer; nesse
caso, perguntar produz um menu de opções ruins. O valor pode ser conservador (0.5–0.9 por banda). A condição de
massa do menu (`TopMass`) é mais importante que `TauAsk`: sem ela, o menu pode não conter a resposta certa.

### 3.3 A tabela de τ, derivada do custo

A tabela não é escolhida no olho; ela sai de `τ = 1 − c_alt / C_erro`, com `c_alt = min(c_ask, c_menu, c_model,
c_refuse)`. Unidade: **1 = um turno do usuário**.

Custos default (produto, não medição). O `c_ask` é o "custo de incomodar" de Horvitz, que defende agir só quando o
valor esperado da ação supera o da inação, considerando o custo de perturbar o usuário
([Horvitz, *Principles of Mixed-Initiative User Interfaces*, CHI 1999](https://doi.org/10.1145/302979.303030)):

| Parâmetro | Valor | Justificativa |
|---|---|---|
| `c_ask` | 1 | uma pergunta, um turno |
| `c_menu` | 1.25 | uma pergunta + leitura de até 5 opções |
| `c_refuse` | 2–3 | a tarefa falha; o usuário reformula ou faz à mão |
| `c_model` | 10 (online) / ∞ (offline) | tokens + latência; offline não há |
| `C_erro(BandRead)` | 5 | resposta errada observável, sem efeito colateral |
| `C_erro(BandReversible)` | 20 | escrita desfeita pelo `/undo`, mas com retrabalho |
| `C_erro(BandExternal)` | 100 | exec/net com efeito fora do repo |
| `C_erro(BandIrreversible)` | ∞ | perda irreparável |

Com `c_alt = 1` (interativo), `τ = 1 − 1/C_erro`; em headless, `c_alt = 2` e o erro custa mais porque não há
corretor humano (`C_erro` ~4× maior):

| Banda | `C_erro` interativo | `τ` interativo | `C_erro` headless | `τ` headless |
|---|---|---|---|---|
| `BandRead` | 5 | **0.80** | 20 | **0.90** |
| `BandReversible` | 20 | **0.95** | 80 | **0.975** |
| `BandExternal` | 100 | **0.99** | 400 | **0.995** |
| `BandIrreversible` | ∞ | **regra dura** | ∞ | **regra dura** |

**As duas leituras do headless.** Se o erro em headless custa o mesmo que no interativo, `c_alt = 2` e
`C_erro = 5` dão `τ = 0.60` para read — baixo demais para um sistema sem humano no loop. A leitura conservadora
(adotada na tabela) diz que, em headless, um erro custa ~4× mais porque ninguém corrige; `C_erro` sobe para
20/80/400 e `τ` fica em 0.90/0.975/0.995. **As duas leituras são defensáveis; a diferença é uma premissa de
produto, não um teorema.** O ponto não é o número; é que a premissa está explícita e pode ser testada.

A tabela usa `c_alt = c_ask = 1` (a alternativa é perguntar). Se a alternativa mais barata for o menu
(`c_menu = 1.25`) ou a recusa (`c_refuse = 2`), `τ` sobe alguns pontos; o P10 mede essa sensibilidade. A função
`Decide` usa a tabela pré-computada; ela não recalcula `c_alt` a cada pedido, porque a diferença é pequena e o
custo de errar a alternativa é menor que o de errar a banda.

**Por que não usar `C_erro` absoluto.** A escala dos custos é arbitrária; só as razões importam. Se todos os custos
dobram, os `τ` não mudam. O que importa é `r_b = C_erro(b) / c_alt`:
`r = 5, 20, 100, ∞` ⇒ `τ = 0.80, 0.95, 0.99, 1`. A análise de sensibilidade (§4.9) varia `r` por 10× e mostra
quantas decisões viram. Se a política é estável em `r ∈ [2r, 10r]`, o custo não é frágil; se não é, os dados não
resolvem o limiar e a escolha conservadora é a única honesta.

**O que o classificador pode entregar.** O score bruto do `nlu.go` capa em 0.97 no melhor caso (regra + NB
concordam, `nlu.go:976`). A menos que o mapa calibrado empurre esse score acima de 0.99 — o que exige dados que
justifiquem —, com `τ = 0.99` para `BandExternal` o cérebro **nunca** age sozinho em exec/net, mesmo com a melhor
evidência. Isso é correto? Para `run_tests` (verificador forte, efeito observável), não: o custo do erro é baixo
porque o verificador pega a falha. A banda de `run_tests` deve ser `BandRead` ou `BandReversible`, não
`BandExternal`, porque o efeito é observável e o verificador é `tests_passed`. É a diferença entre "exec" e "exec
com efeito externo". O metadado `Effects` + `Verifier` resolve; a string `risk: med` não.

### 3.4 Modos: interativo, headless, online

O mesmo `p` e a mesma banda levam a decisões diferentes conforme o conjunto de ações:

| Modo | `CanAsk` | `CanModel` | Alternativa típica |
|---|---|---|---|
| Interativo offline | true | false | perguntar/menu; recusar |
| Headless offline (`-p --offline`) | false | false | recusar |
| Interativo online | true | true | perguntar/menu; handoff |
| Headless online (`-p`) | false | true | handoff; recusar |

O código já tem essa informação: `env.Ask != nil` (`brain.go:139`), `env.AskText != nil` (`methods.go:1225`),
`env.Profile.Online` (`brain.go:242-243`). O que falta é usá-la na decisão, não só no callback. Em headless, o
`brChoose` imprime um menu e retorna `tkNeedsInput`; a política deve transformar isso em `DecHandoff`/`DecRefuse`
sem imprimir opções que ninguém pode escolher.

### 3.5 Menu vs. pergunta vs. handoff

**Menu.** É uma rejeição com espaço de resposta `{1..k}`. O valor esperado do menu é
`(1 − p_top) · C_erro`, aproximadamente, se o usuário escolhe certo; o custo é `c_menu`. Então o menu vale quando
`(1 − p_top) · C_erro > c_menu`, que é a mesma desigualdade de Chow com `c_menu` no lugar de `c`. A condição extra é
**cobertura**: o menu só funciona se a resposta certa estiver entre as `k` opções. Com a NLU atual, `p` é
condicionado ao conjunto de empatados (`nlu.go:993`, `classify(canon, tied)`), não é uma distribuição sobre as 50
intenções; a massa top-k **não está disponível**. O `niTop` (`nameindex.go:710`) também não devolve massa, só
candidatos dentro de `fzSlip`. Sem essa massa, o menu é um chute de cobertura. P8 expõe a massa; até lá, use o menu
só quando `brPick` já falhou por empate *e* o número de candidatos é pequeno (≤5), que é o que o código faz.

**Pergunta.** É uma rejeição com espaço livre. Vale quando há um slot obrigatório vazio (`Intent.Missing`,
`nlu.go:54`) e a resposta do usuário é confiável. O custo é `c_ask`; o benefício é reduzir a ambiguidade. Se o slot
não é o gargalo (o problema é a intenção), perguntar o slot não ajuda; é preciso perguntar a intenção (menu de
intenções) ou handoff. O código já separa: `brAskSlot` para slot, `brChoose` para candidatos. A política deve
separar também.

**Handoff.** É uma ação com custo `c_model` e confiabilidade `p_model`. A regra não é "se o cérebro falhou, chama o
modelo"; é "se o custo esperado do modelo é menor que o do cérebro". Se `p_model ≈ 1`, o handoff vale quando
`p_brain < 1 − c_model/C_erro`, a mesma Chow com `c_model`. Se `p_model` é 0.8, o custo esperado é
`c_model + 0.2·C_erro`, e o handoff pode ser pior que perguntar. O código atual trata o modelo como fallback
(`offline.go:379` e `brain.go:610`), não como ação com custo; é uma heurística razoável, mas não é a regra ótima.
Medir `p_model` é difícil; use uma priori conservadora e não sobre-engenhe.

**Recusa.** É a ação "nenhuma das outras". Custa `c_refuse` (a tarefa falha). Em headless offline, é a única
alternativa; em interativo, raramente é ótima se há pergunta. O `brNotUnderstood` (`brain.go:214`) é uma recusa
honesta com lista de capacidades; é o modelo certo para `DecRefuse`.

### 3.6 O caso "p alto, sem verificador"

Um método sem `Verify` (`len(m.Verify)==0`) pode "suceder" sem evidência; o `brVerify` retorna `unavailable` e o
status é `tkUnverified` (`brain.go:363-370`, `methods.go:1680+`). A política deve tratar isso como uma banda acima:
um `BandReversible` sem verificador vira `BandExternal`; um `BandExternal` sem verificador vira
`BandIrreversible` (pergunta sempre). A razão é simples: sem verificador, o `false_success` não é detectável; o
custo de agir errado inclui a confiança perdida, que é o evento mais caro. O `bandOf` acima já sobe uma banda; a
regra pode ser mais dura: se `len(m.Verify)==0` e a banda é `>= BandExternal`, force `DecAsk`/`DecRefuse`.

### 3.7 O caso "p alto, mas o resolvedor está incerto"

O `p_intent` não vê o resolvedor. Exemplo: "apaga o config" com `p_intent = 0.97` e três `config.*`; o `brPick`
vai perguntar (bom), mas a decisão de agir no arquivo é do resolvedor, não da intenção. A política precisa de
`p_slot`: a probabilidade de o candidato escolhido ser o certo. Até o P4 (calibração do resolvedor), use uma
**tabela conservadora** baseada nas regras do `brPick`:

| Regra do `brPick` | `p_slot` conservador |
|---|---|
| exato e único (`exact==1 && c[0].Exact`) | 0.99 |
| único dentro do limite de typo (`brSafeTypo`) | 0.90 |
| líder com margem ≥ 16 | 0.85 |
| único longe / empate | 0.50 (perguntar) |

Esses números não são medidos; são um **piso** conservador. O P4 os substitui por um mapa calibrado. O importante é
que a decisão use a cota inferior `max(0, p_intent + p_slot − 1)` (ou uma calibração fim-a-fim); hoje o `brPick`
decide sozinho, com uma regra dura, e o `p_intent` não entra.

**O resolvedor não cobre as operações destrutivas.** `open_file`, `explain`, `tool_info`, `tool_status` e
`connect_host` usam `resolve_name` + `choose`; `delete_file`, `rename` e `create` vão direto para `file_op` com
`$slot.target` (`methods/delete_file.json`, `rename.json`, `create.json`). O `brPick` não é chamado; um alvo
ambíguo não é desambiguado. A banda `BandIrreversible`/`BandExternal` protege contra agir sem confirmação, mas não
contra escolher o arquivo errado **com** confirmação: o usuário confirma o comando `rm config`, e o shell recebe o
texto cru — se não houver um arquivo exatamente `config`, o `rm` falha; se houver, apaga esse, que pode não ser o
que o usuário quis. P4 deve cobrir essas intenções também; até lá, a confirmação exigida pela banda (e a regra dura
de irreversível) é a defesa. É um exemplo de que a política de decisão não substitui a resolução de nomes; ela só
decide **se** age, não **em quê**.

---

## 4. Curva risco–cobertura e como escolher τ sem overfit

### 4.1 Definições

Para um seletor `g(x) ∈ {0,1}` (age ou rejeita) e um classificador `f`, a **cobertura** é
`coverage(g) = P(g(X) = 1)` e o **risco seletivo** é
`risk(g) = P(f(X) ≠ Y | g(X) = 1)`. A curva risco–cobertura é o conjunto de pares `(risk(g_τ), coverage(g_τ))`
ao variar `τ` ([El-Yaniv & Wiener, *On the Foundations of Noise-free Selective Classification*, JMLR
11:1605–1641, 2010](https://jmlr.org/papers/v11/el-yaniv10a.html)). A área sob a curva (AURC) resume o trade-off;
menor é melhor ([Geifman & El-Yaniv, *Selective Classification for Deep Neural Networks*, NeurIPS 2017,
arXiv:1705.08500](https://arxiv.org/abs/1705.08500)).

No jazz, `coverage` é o `no_llm_rate` (fração resolvida sem modelo) e `risk` é a taxa de erro entre as resolvidas.
Mas há uma complicação: o custo do erro depende da banda. A curva de uma banda só não basta; a política completa
tem um **custo esperado por pedido**:

```
Custo(τ) = (1/N) Σ_i [ age? (1 − correct_i)·C_erro(banda_i) : c_alt_i ]
```

Reporte as duas coisas: as curvas por banda (interpretáveis) e o custo total (a decisão). O gate deve usar o custo
total, não a acurácia.

### 4.2 O estimador ingênuo é otimista — prova

Suponha 75 tarefas e uma grade de 20 valores de `τ`. Para cada `τ`, o risco empírico é uma variável binomial. O
mínimo dos 20 riscos empíricos **tende a ser menor** que o risco verdadeiro do `τ` escolhido, porque você escolheu o
mínimo de 20 realizações ruidosas. É o "winner's curse" / viés de seleção. A expectativa do mínimo de `k` binomiais
é menor que a média; com `n` pequeno, o viés é grande. O mesmo vale para a cobertura: escolher o `τ` que maximiza a
cobertura empírica com risco zero é escolher o ponto mais sortudo, não o ponto ótimo. Referências:
[Cawley & Talbot, *On Over-fitting in Model Selection and Subsequent Selection Bias in Performance Evaluation*,
JMLR 11:2079–2107, 2010](https://www.jmlr.org/papers/v11/cawley10a.html);
[Varma & Simon, *Bias in error estimation when using cross-validation for model selection*, BMC Bioinformatics
7:91, 2006](https://doi.org/10.1186/1471-2105-7-91).

**Consequência:** nunca escolha `τ` no mesmo conjunto em que você mede o risco. Use validação cruzada aninhada, ou
um bound de confiança que penalize a seleção. O `forge_gate` atual (`forge_gate.go:143-180`) usa um único split e
um `gateEps=0.01`; ele não escapa do winner's curse, e o `eps` é menor que o ruído. O §6.8 detalha.

### 4.3 LOO aninhado e k-fold agrupado

O protocolo mínimo:

1. **Deduplique / agrupe near-duplicates.** Os 644 exemplos são paráfrases curadas; um split aleatório vaza
   paráfrases entre treino e teste. Agrupe por cluster de texto normalizado (ou por família de template) e mantenha
   o cluster inteiro em um lado. O vazamento por duplicatas é uma das causas mais comuns de resultados otimistas
   ([Kapoor & Narayanan, *Leakage and the Reproducibility Crisis in ML-based Science*, arXiv:2207.07048,
   2023](https://arxiv.org/abs/2207.07048)).
2. **K-fold agrupado, repetido.** 5 folds × 5 repetições, estratificado por intenção, com os clusters como grupo.
   Para o mapa de calibração, o LOO completo (n treinos) é O(n²) e caro no Dell; o k-fold é O(k·n). Use k-fold no
   forge (amd64). O `nuTrain(skip)` (`nlu.go:1363`) já aceita o predicado de exclusão, então o mecanismo existe.
3. **Predições out-of-fold.** Para cada exemplo, o `p` usado na avaliação é o de um mapa ajustado sem aquele fold.
   O conjunto de predições out-of-fold é a base da curva risco–cobertura.
4. **Escolha de `τ` com bound.** Para cada `τ` da grade, conte `k` erros em `n` aceitos. Calcule o limite superior
   de Clopper–Pearson `U(k, n, δ)` (a solução de `Σ_{j=0}^{k} C(n,j) b^j (1−b)^{n−j} = δ`; é o bound exato usado
   pelo SGR de Geifman & El-Yaniv, Lemma 3.1, e por
   [Gascuel & Caraux, *Distribution-free performance bounds with the resubstitution error estimate*, Pattern
   Recognition Letters 13(11):757–764, 1992](https://doi.org/10.1016/0167-8655(92)90125-J)). Escolha o **menor**
   `τ` com `U ≤ α_b`. Isso maximiza a cobertura sujeito à restrição de risco com confiança `1−δ`.
5. **Correção de múltiplas comparações.** A grade tem `G` valores; use `δ/G` (Bonferroni) ou o controle de FWER do
   Learn-then-Test ([Angelopoulos, Bates, Candès, Jordan & Lei, *Learn then Test: Calibrating Predictive Algorithms
   to Achieve Risk Control*, arXiv:2110.01052, 2022](https://arxiv.org/abs/2110.01052)). O LTT é a forma mais
   direta: trate cada `τ` como uma hipótese nula "risco > α" e rejeite com FWER.
6. **Faixa de guarda.** `τ_final = τ* + um passo da grade` (ou o ponto médio para o próximo candidato). A faixa
   absorve o fato de que o bound é sobre a amostra de calibração, não sobre o futuro. É a diferença entre "o
   limite superior é ≤ α" e "o limite superior é ≤ α com folga".
7. **Bootstrap por grupo.** Reamostre os grupos (não os pontos) 1000×; recalcule a curva e o custo; reporte os
   percentis 2,5/97,5. Com 75 tarefas, o intervalo é largo; reportá-lo é mais honesto que reportar um ponto.
8. **Holdout intocado.** As 75 tarefas e as 100 frases cegas são o teste de aceitação, não o conjunto de ajuste.
   Rode uma vez por candidato; se você ajustar nelas, elas viram treino.
9. **Pré-registro.** Fixe a grade, o `δ`, o seed do split e a matriz de custo antes de olhar o resultado. O
   `forge_gate.Split` já é determinístico por `(seed, ID)` (`forge_gate.go:92-134`); o seed é uma escolha do
   pesquisador, então pré-registre.
10. **Monitoramento.** Em produção, o ledger de sinais (`signals.go`) detecta deriva; a recalibração exige um novo
    gate e um novo holdout.

### 4.4 SGR, LTT e conformal risk control: o que cada um garante

| Método | Garantia | Hipótese | Custo |
|---|---|---|---|
| **SGR** (Geifman & El-Yaniv 2017) | `P(risco verdadeiro ≤ bound) ≥ 1−δ`, bound exato de Clopper–Pearson, união sobre `log2(m)` iterações | i.i.d. entre calibração e teste | binário search; barato |
| **LTT** (Angelopoulos et al. 2022) | FWER sobre a grade; risco ≤ α com prob. `1−δ` | i.i.d.; risco monótono ou não | testa a grade; barato |
| **Conformal risk control** (Angelopoulos et al. 2022, arXiv:2208.02814) | `E[loss] ≤ α` para loss monotônico; tight até `O(1/n)` | exchangeability | escolhe `λ` no conjunto de calibração; barato |
| **Conformal prediction** (Vovk et al. 2005; Lei et al. 2018) | cobertura marginal `1−α` | exchangeability | quantil empírico; barato |

Todos os três primeiros são aplicáveis ao jazz. O SGR é o mais próximo do que já existe (o bound exato é o mesmo
Clopper–Pearson); o LTT é o mais simples de implementar com uma grade; o conformal risk control é o mais geral se o
loss for monotônico no parâmetro. O risco seletivo em geral cai quando `τ` sobe, mas isso **não é garantido** com
um score ruidoso; verifique a monotonicidade empírica antes de invocar o CRC. **Nenhum deles garante nada
sob distribution shift**; a garantia é condicional à exchangeability. Os 644 exemplos curados e os pedidos reais não
são exchangeable; o bound é sobre a amostra de calibração, não sobre o futuro. Para drift, use os bounds ponderados
de [Barber, Candès, Ramdas & Tibshirani, *Conformal prediction beyond exchangeability*, Annals of Statistics
51(2):816–845, 2023](https://doi.org/10.1214/23-AOS2276) ou monitore e recalibre.

### 4.5 Faixa de guarda e o bound exato

O bound de Clopper–Pearson é a solução de `Σ_{j=0}^{k} C(n,j) b^j (1−b)^{n−j} = δ` para `b`. Em Go, no eval (não no
runtime):

```go
// cpUpper devolve o limite superior 1−δ para k erros em n tentativas.
func cpUpper(k, n int, delta float64) float64 {
    if n == 0 { return 1 }
    lo, hi := float64(k)/float64(n), 1.0
    for i := 0; i < 60; i++ {
        mid := (lo + hi) / 2
        if binomCDF(k, n, mid) > delta { lo = mid } else { hi = mid }
    }
    return hi
}

// binomCDF = P(X ≤ k) para X ~ Bin(n, p).
func binomCDF(k, n int, p float64) float64 {
    if p <= 0 { if k >= 0 { return 1 }; return 0 }
    if p >= 1 { if k >= n { return 1 }; return 0 }
    var s float64
    for j := 0; j <= k; j++ {
        s += math.Exp(math.Lgamma(float64(n+1)) - math.Lgamma(float64(j+1)) -
            math.Lgamma(float64(n-j+1)) +
            float64(j)*math.Log(p) + float64(n-j)*math.Log1p(-p))
    }
    return s
}
```

`n ≤ 644`, `60` iterações, `O(n)` por avaliação: ~40 µs por `τ`; a grade inteira em ~1 ms. É eval-only; o Dell
não paga. O `math.Lgamma` está na stdlib.

**Por que não Wilson.** O intervalo de Wilson é mais estreito e mais estável, mas é uma aproximação; o
Clopper–Pearson é exato e é o que o SGR usa. Para o gate, o exato é melhor; para um relatório rápido, Wilson serve.
A diferença com `n=75` é pequena, mas o exato é defensável.

### 4.6 Bootstrap por grupo

O bootstrap ingênuo (reamostrar pontos) subestima a variância quando há correlação dentro de grupos (paráfrases,
mesma tarefa, mesmo repo); o bootstrap por grupo é a correção padrão
([Efron & Tibshirani, *An Introduction to the Bootstrap*, Chapman & Hall/CRC 1993](https://doi.org/10.1201/9780429246593)).
Reamostre **grupos**:

```go
func bootstrapCost(rows []Row, groups []string, B int, seed int64) (lo, hi float64) {
    rng := rand.New(rand.NewSource(seed))
    idx := map[string][]int{}
    for i, g := range groups { idx[g] = append(idx[g], i) }
    keys := make([]string, 0, len(idx))
    for g := range idx { keys = append(keys, g) }
    out := make([]float64, B)
    for b := 0; b < B; b++ {
        var sample []Row
        for range keys {
            g := keys[rng.Intn(len(keys))]
            for _, i := range idx[g] { sample = append(sample, rows[i]) }
        }
        out[b] = totalCost(sample, policy)
    }
    sort.Float64s(out)
    return out[B*25/1000], out[B*975/1000]
}
```

Com 75 tarefas e ~20 grupos, o bootstrap tem ~20 unidades de reamostragem; o intervalo é largo. Reporte o número de
grupos, não só o de linhas.

### 4.7 O tamanho da amostra: 75 tarefas, 0/75, regra de três

Com 0 eventos em `n` tentativas, o limite superior de 95% é ~`3/n`
([Hanley & Lippman-Hand, *If Nothing Goes Wrong, Is Everything All Right? Interpreting Zero Numerators*, JAMA
249(13):1743–1745, 1983](https://doi.org/10.1001/jama.1983.03330370053031)). Para `n=75`, `3/75 = 4%`. O valor exato
de Clopper–Pearson é `1 − 0.05^{1/75} ≈ 3,9%`. Ou seja: **0 `false_success` em 75 tarefas é compatível com uma taxa
verdadeira de 4%**. Para certificar 1% com 0 eventos, são necessárias `n ≥ ln(0,05)/ln(0,99) ≈ 299` tarefas. Para
certificar 1% numa banda com 20 tarefas, impossível: o limite é ~14%.

Isso não é um detalhe acadêmico; é o que impede vender "0 false_success" como "seguro". O eval de 75 tarefas é um
**teste de fumaça**: ele pega regressões grandes, não certifica um SLA. Para certificar, precise de ~300 tarefas ou
de monitoramento em produção com um denominador amostrado. O `false_success` por banda, com 75 tarefas divididas em
4 bandas, tem ~19 tarefas/banda; o limite de 0 eventos é ~15%. Qualquer `τ` por banda escolhido nesse tamanho é
ruído.

**Quantos τ dá para estimar?** Com 75 tarefas, não dá para estimar 8 `τ` livres (4 bandas × 2 modos) com precisão
útil. O que dá para estimar é a **ordem** e a **forma** da curva; os valores absolutos vêm do modelo de custo.
Recomendação: estime 2–3 pontos (read, reversible, external) e derive os demais do custo; trate a diferença entre
modos como uma premissa declarada, não como um parâmetro ajustado. Se o eval não separa dois `τ` com o IC, use o mais
conservador. Um `τ` por banda só se justifica quando a banda tem `n` suficiente para o IC separar; caso contrário,
é a matriz de custo disfarçada de estatística.

### 4.8 Protocolo de validação em 8 passos (resumo operacional)

1. Congele a matriz de custo e o mapa de bandas em `docs/` ou num JSON versionado.
2. Deduplique e agrupe os 644 exemplos; separe clusters, não linhas.
3. Rode 5×5 k-fold agrupado; colete `(p_out_of_fold, correct, channel, group)`.
4. Ajuste o mapa de calibração no treino de cada fold; aplique no teste; pool out-of-fold.
5. Para cada banda, varra `τ`; escolha o menor `τ` com `cpUpper(k,n,δ/G) ≤ α_b`; adicione a faixa de guarda.
6. Bootstrap por grupo 1000×; reporte cobertura, risco, custo e AURC com IC 95%.
7. Rode o holdout de 75 tarefas + 100 frases cegas **uma vez**; reporte `false_success`, `false_blocks`,
   `no_llm_rate`, perguntas e custo.
8. Passe o asset pelo `forge_gate` estendido (§6.8); monitore em produção com o ledger amostrado (§7 P7).

### 4.9 Teste de sensibilidade do custo

A matriz de custo é uma premissa. Varie `r_b = C_erro(b)/c_alt` por um fator de 2× a 10× e conte quantas decisões
viram no corpus. Se a fração que vira é pequena (digamos < 5%), a política é robusta à premissa; se é grande, os
dados não resolvem o limiar e a escolha conservadora é a única honesta. O comando proposto (`jazz eval policy
--cost-grid`) reporta:

```
r:      5    10    20    50   100
τ:   0.80  0.90  0.95  0.98  0.99
cov:  ...   ...   ...   ...   ...
fs:   ...   ...   ...   ...   ...
flip: ... (fração de decisões que mudam em relação ao default)
```

Sem esse teste, a tabela de τ é superstição numérica com aparência de teoria.

---

## 5. Grouping loss: por que um score igual não basta

### 5.1 A decomposição

Para uma scoring rule estritamente própria `φ` (Brier ou log-loss), a divergência se decompõe em

```
E[d_φ(S, Y)] = E[d_φ(S, C)] + E[d_φ(C, Q)] + E[d_φ(Q, Y)]
                 calibração        grouping       irredutível
```

onde `C = E[Q | S]` é o score calibrado e `Q` é a posterior verdadeira
([Perez-Lebel et al. 2023, Eq. 6](https://arxiv.org/abs/2210.16315)). O **grouping loss** é `E[d_φ(C, Q)]`: a
variação da posterior verdadeira **dentro** do nível de confiança. Para o Brier, ele é uma variância:
`GL(S) = E[V_h[Q | S]]`, com `h` a entropia negativa. Para uma partição `R` do espaço de features, o teorema 4.1
decompõe `GL = GL_explained + GL_residual`, e `GL ≥ GL_explained ≥ 0`; o `GL_explained` é o que dá para estimar, e
é um **limitante inferior** do verdadeiro. A implicação prática: um classificador perfeitamente calibrado ainda
pode ter scores que não são posteriores individuais; dois pedidos com `p=0.9` podem ter taxas de acerto diferentes.

### 5.2 O caso do jazz, concretamente

Exemplos com a mesma `p` e sucessos diferentes (valores de `P(sucesso)` ilustrativos, não medidos):

| Pedido | Canal | `p_intent` | O que muda | `P(sucesso)` ilustrativo |
|---|---|---|---|---|
| "abre o parser.go" | regra + NB concorda | 0.97 | alvo exato, arquivo existe | ~0.99 |
| "abre o config" | regra + NB concorda | 0.97 | 3 candidatos `config.*` | ~0.5 (ou pergunta) |
| "apaga o config" | regra + NB concorda | 0.97 | `file_op delete` com o slot cru; sem resolvedor | ~0 ou 1, sem desambiguação |
| "roda os testes" | regra + NB concorda | 0.97 | projeto sem runner | ~0.0 (bloqueia) |
| "roda os testes" | regra + NB concorda | 0.97 | runner detectado | ~0.95 |

O `p_intent` é o mesmo; o que muda é o **slot/resolvedor**, a **aplicabilidade do método** e a **banda**. Isso é
grouping loss puro: a confiança da intenção é cega ao contexto que decide o sucesso. A correção não é "calibrar
melhor a intenção"; é condicionar em features de contexto e/ou tratar os estágios separadamente.

### 5.3 Quais features de contexto

A pergunta "quais features?" tem uma resposta principiada: as que (a) estão disponíveis **antes** da decisão, (b)
têm um caminho causal para o sucesso, (c) não fragmentam os 644 exemplos abaixo de ~30 por célula, e (d) reduzem o
`GL_explained` além do ruído do bootstrap. Na seleção, use a regra de 1 erro-padrão — prefira o modelo mais simples
dentro de 1 SE do melhor — para não escolher ruído
([Hastie, Tibshirani & Friedman, *The Elements of Statistical Learning*, 2ª ed., Springer 2009](https://doi.org/10.1007/978-0-387-84858-7)).
Candidatas, em ordem de custo/benefício:

| # | Feature | Onde já existe | Custo | Por que | Risco de overfit |
|---|---|---|---|---|---|
| 1 | **Canal** (`rule-only`, `rule+nb-agree`, `rule+nb-veto`, `nb-only`, `tie`, `typo-only`, `vetoed`, `multi`) | implícito em `Evidence` (`nlu.go:970`, `:980`, `:1001`); falta campo | zero | as heurísticas de confiança são por canal; é a maior fonte de heterogeneidade | baixo (8 níveis) |
| 2 | **Slot obrigatório vazio** (`len(in.Missing) > 0`) | `Intent.Missing` (`nlu.go:54`) | zero | sem slot, o método pergunta ou falha; `p` alto é falso | baixo (2 níveis) |
| 3 | **OOV fraction** (fração de n-grams em baldes globalmente vazios) | `nuTrain`/`predict` (`nlu.go:1363-1427`); não exposto | ~2 µs/pedido | mede "o NB não tem evidência"; `p` é dominado pelo prior | médio (3 bins) |
| 4 | **Typo-only** | `Intent.TypoOnly` (`nlu.go:58`, `:1052`) | zero | a confiança é capada em 0.5, mas o canal é distinto | baixo (2 níveis) |
| 5 | **Residual count** (palavras fora da gramática) | `residualCount` (`nlu.go:1121`), usado em `:1063` | zero | pedido livre é trabalho de modelo; o ×0.6 pode não bastar | médio (3 bins) |
| 6 | **Features do resolvedor** (`Dist`, `Exact`, margem, `#cands`, extensão, hint, recência) | `niEntity` (`nameindex.go`), `brPick` (`methods.go:1132`) | ~1 µs | o `p_intent` não vê o slot; é o maior grouping loss do jazz | médio-alto (mapa à parte) |
| 7 | **Aplicabilidade do método / verificador** | `mhApplicable` (`methods.go`), `m.Verify` | zero | `p_intent` não vê se o método roda; as `pre` veem, mas a confiança não | baixo (usar na política, não no mapa) |
| 8 | **Família de repo/tarefa** | `Repo_Sig` em `signals.go`; `fpProject` | zero | prior por repo; útil com shrinkage | alto (muitos níveis) |
| 9 | **Língua** (pt/en) | detectável do texto | zero | o léxico é pt-BR; inglês pode ser menos calibrado | médio (2 níveis) |
| 10 | **Histórico de correções** | `signals.jsonl` | leitura O(n) | sinal de dificuldade por intenção/repo | alto (não-iid, feedback loop) |

**Recomendação:** adicionar 1, 2 e 3 primeiro; medir o `GL_explained` com uma partição `canal × 3 faixas de p` e
depois `canal × missing × oov`. Só adicionar 6 (resolvedor) como um **mapa separado**, porque o alvo é outro
(`P(candidato certo)`), e 7 como **fato da política**, não como feature do mapa. Não adicionar 8–10 sem um esquema
de amostragem e shrinkage; com 644 exemplos, eles overfittam.

**O que não é feature:** o resultado (`correct`), o método que falhou, a correção do usuário, o tempo de resposta, o
modelo que foi chamado. Todos são pós-decisão; usá-los é vazamento. A taxonomia de 8 tipos de vazamento está em
[Kapoor & Narayanan 2023](https://arxiv.org/abs/2207.07048); o jazz é suscetível ao tipo "feature disponível só
depois do outcome" e ao "near-duplicate entre splits".

### 5.4 Como medir o grouping loss com 644 exemplos

O estimador plug-in do `GL_explained` é enviesado para cima com `n` pequeno (Perez-Lebel §C.6). Duas opções
honestas:

1. **Split-half.** Calcule as médias por grupo em uma metade, avalie na outra; a diferença entre a acurácia do grupo
   e a acurácia do bin evita o viés do plugin (mas tem variância alta). Repita 100× e reporte a média e o IC.
2. **Estimador de Perez-Lebel §4.4** (debiased). É mais eficiente, mas mais código; se for implementar, siga o
   paper e valide contra o split-half.

O número que importa não é o `GL` absoluto; é a **fração do Brier** que ele explica. Se `GL_explained / Brier` é
pequeno (digamos < 5%), adicionar features de contexto não compensa; se é grande (> 20%), a calibração sozinha não
vai resolver a decisão e as features são o caminho. Com 644 exemplos, o IC dessa fração é largo; trate como
diagnóstico, não como gate.

### 5.5 Contraexemplo: o produto de estágios não é calibrado

Suponha `p_intent = 0.9` e `p_slot = 0.9`. Se os erros fossem independentes, `p_sucesso = 0.81`. Mas se toda
intenção errada arrasta o slot errado (correlação positiva), `P(ambos) = P(intent) = 0.9`; se os erros são
mutuamente exclusivos (correlação negativa), `P(ambos) = 0.8`. As cotas de Fréchet–Hoeffding dão
`[0.8, 0.9]`. O produto 0.81 está dentro do intervalo, mas não é o valor; é um ponto sob uma hipótese não testada.
Para a decisão, use a cota inferior 0.8 (conservadora) ou calibre o fim-a-fim. O mesmo vale para `p_method`: um método
com `p=0.9` e um resolvedor com `p=0.9` não dão 0.81 de sucesso; dão algo entre 0.8 e 0.9, e a correlação é
desconhecida. **Nunca use o produto como se fosse uma probabilidade calibrada.**

---

## 6. Amarração ao `calib.go` e ao `mhMethod.Risk`

### 6.1 O alvo da calibração, por canal

| Canal | Alvo `Correct` | Onde medir |
|---|---|---|
| `rule-only` | `intent == gold` | 644 exemplos, LOO/k-fold |
| `rule+nb-agree` | `intent == gold` | idem |
| `rule+nb-veto` | `intent == gold` | idem |
| `nb-only` | `intent == gold` | idem |
| `tie` | `intent == gold` | idem |
| `typo-only` | `intent == gold` | idem; poucos exemplos |
| `vetoed` | `intent == gold` | idem; poucos |
| `multi` | todas as partes certas | tarefas multi (4 no eval) |
| `resolve` | candidato escolhido == gold | corpus sintético + logs |
| `method` | tarefa verificada | 75 tarefas (esparso) |

Não misture alvos. Um mapa ajustado em `intent` não calibra `resolve`; um mapa ajustado em `resolve` não calibra
`method`. O `CalibSample{P, Correct}` (`calib.go:72-75`) serve para todos, mas o **conjunto** e o **canal** mudam.
Adicione `Channel` e `Group` ao ponto de calibração (ou mantenha fora e agrupe).

### 6.2 Platt vs. isotônico vs. beta: quando cada um

| Mapa | Parâmetros | Quando | Dados mínimos (regra de bolso) | Fonte |
|---|---|---|---|---|
| Identidade | 0 | fallback; canal com `n < 30` | — | — |
| Temperatura | 1 | correção global; um único canal | 30+ | Guo et al. 2017 |
| Platt | 2 | canal com distorção simétrica; NB overconfident | 30–50+ | Platt 1999 |
| Beta | 3 | NB com scores nos extremos; assimetria | 100+ | Kull et al. 2017 |
| Isotônico (PAV) | `k` blocos | canal grande, forma desconhecida | 150–200+ | Zadrozny & Elkan 2002; Ayer et al. 1955 |

O `calib.go` tem Platt (`:220`) e isotônico (`:281`); não tem beta nem temperatura explícita (temperatura é Platt
com `a=1/T, b=0`). A regra de bolso é conservadora de propósito: com 644 exemplos divididos em 8 canais, a média é
~80/canal, mas a distribuição é desigual; alguns canais têm <30. Para esses, use identidade ou shrinkage ao mapa
global. Isotônico com 16 nós e 50 pontos tem ~3 pontos/nó; é ruído monotônico, não calibração
([Niculescu-Mizil & Caruana, ICML 2005](https://doi.org/10.1145/1102351.1102430), que documentam o overfit do
isotônico com dados escassos).

**Shrinkage.** Para um canal com `n` pontos, combine o mapa do canal com o global:
`p_cal = w·p_channel + (1−w)·p_global`, `w = n/(n+k)`, `k ≈ 50`. É empirical Bayes simples
([Efron & Morris, *Data Analysis Using Stein's Estimator and Its Generalizations*, JASA 70(350):311–319,
1975](https://doi.org/10.1080/01621459.1975.10479864); [Gelman et al., *Bayesian Data Analysis*, 3ª ed., CRC
Press 2013](https://doi.org/10.1201/b16018)). O `scores.go:49-54` já usa uma versão disso para modelos
(`(pass+1)/(pass+fail+2)`, com `bgScoreMinObs=3`); a diferença é que lá é a **média** posterior, aqui precisamos do
**limite inferior** para segurança. Não copie a média; use o quantil inferior ou o Clopper–Pearson.

### 6.3 O asset `nlu/calib.json`

Schema proposto:

```json
{
  "version": 1,
  "fit": {"n": 644, "method": "platt", "date": "2026-11-04", "seed": 7},
  "channels": {
    "rule+nb-agree": {"kind": "platt", "a": 1.12, "b": -0.41, "clip": [0.02, 0.99]},
    "rule-only":     {"kind": "platt", "a": 0.93, "b": -0.12, "clip": [0.02, 0.98]},
    "nb-only":       {"kind": "platt", "a": 0.61, "b": -0.05, "clip": [0.01, 0.95]},
    "tie":           {"kind": "iso", "pts": [[0.55, 0.52], [0.70, 0.61], [0.85, 0.74]], "clip": [0.01, 0.95]},
    "typo-only":     {"kind": "identity"},
    "default":       {"kind": "identity"}
  }
}
```

- `kind: platt`: `p_cal = sigmoid(a·logit(p) + b)`, com `p` clampado em `[ε, 1−ε]` (`calib.go:88-96`).
- `kind: iso`: `IsotonicApply` (`calib.go:326-353`), com **âncoras** (ver §6.5). O `tie` com 3 pontos no exemplo é
  só ilustração do formato; na prática, 3 pontos não justificam isotônico — use Platt.
- `clip`: limita o resultado; evita que um canal com poucos dados produza 0.999.
- `default: identity`: fallback se o canal não está no asset; comportamento atual.
- `fit`: proveniência (n, método, data, seed). O `forge_gate` usa para auditar.

Tamanho: 8 canais × ~100 B = <1 KB; isotônico com 16 pontos × 8 B = 128 B por canal. No Dell, carregar no `init`
é irrelevante. A aplicação em `Parse` é O(1) (Platt) ou O(log k) (isotônico, k≤32); ~20–60 ns, sem alocação.

### 6.4 Onde aplicar no `Parse`

O `Parse` (`nlu.go:928-1070`) termina com `in.Confidence = math.Round(conf*100)/100`. A aplicação do mapa deve ser
**depois** de todos os fatores heurísticos e **antes** do `return`:

```go
in.Confidence = math.Round(conf*100) / 100
if cm := calibGet(); cm != nil {
    in.PRaw = in.Confidence
    in.Confidence = cm.Apply(in.Channel, in.Confidence)
    in.PCal = in.Confidence
}
```

O `Channel` precisa ser setado nos ramos:

- `len(matches)==0` → `nb-only` (ou `vetoed` se `vetoed[cls]`);
- regra única → `rule+nb-agree` se `cls==choice`, `rule+nb-veto` se `none` com `p>0.9`, senão `rule-only`;
- empate → `tie`;
- typo-only → `typo-only` (sobrepõe o canal);
- multi → `multi`.

Hoje o canal está implícito nas strings de `Evidence` (`nlu.go:970`, `:980`, `:1001`); formalizar um campo é
barato e necessário para P1/P2/P3.

### 6.5 O bug do clamp do `IsotonicApply` e as âncoras

`IsotonicApply` (`calib.go:326-353`) faz:

```go
if p <= fit[0].P { return fit[0].Y }
if p >= fit[len(fit)-1].P { return fit[len(fit)-1].Y }
```

Se o último ponto do fit é `(0.85, 0.74)`, um `p` bruto de 0.97 vira 0.74. Isso é conservador (menos ações), mas
pode destruir a cobertura dos casos de alta confiança. Duas correções:

1. **Âncoras explícitas**: adicionar `(0,0)` e `(1,1)` ao fit se não existirem. Isso faz o mapa passar pelos
   extremos, mas pode ser otimista se os dados não suportam.
2. **Clipe por canal**: `clip` no asset, com o limite superior escolhido pelo LOO (não pelo treino). O `clip` é a
   forma mais honesta de dizer "não sei calibrar acima de 0.95 com 30 pontos".

Recomendação: use Platt para os canais pequenos (não tem o problema de clamp; o logit é finito e o sigmoid satura
suavemente), isotônico só para o canal global com n≥200 e com `clip` + âncoras. O `PlattApply` já clampa `p` em
`[1e-6, 1−1e-6]`; o `a` pode ficar negativo em dados degenerados, então adicione um guarda: se `a <= 0`, use
identidade. Isso não está no `calib.go` hoje.

### 6.6 ECE com bins por quantil

`ReliabilityBins` (`calib.go:114-155`) usa bins de largura fixa. Com confianças concentradas em 0.9/0.97, os bins
baixos ficam vazios e o ECE é dominado por um ou dois bins. O ECE é definido como
`Σ_b (|B_b|/N)·|acc(B_b) − conf(B_b)|`
([Naeini, Cooper & Hauskrecht, *Obtaining Well Calibrated Probabilities Using Bayesian Binning*, AAAI 2015](https://ojs.aaai.org/index.php/AAAI/article/view/9602)).
O correto com `n < 10^3` é **bins por quantil** (mesma massa por bin), como em
[Vaicenavicius et al., *Evaluating model calibration in classification*, AISTATS 2019](https://proceedings.mlr.press/v89/vaicenavicius19a.html).
Proposta:

```go
func QuantileBins(samples []CalibSample, nbins int) []ReliabilityBin {
    sorted := append([]CalibSample(nil), samples...) // não muta o caller
    sort.Slice(sorted, func(i, j int) bool { return sorted[i].P < sorted[j].P })
    // divide em nbins blocos de tamanho ~n/nbins; Lo/Hi = min/max P do bloco
    // Conf/Acc = médias; N = tamanho
}
```

O ECE por quantil é comparável entre canais com distribuições diferentes; o de largura fixa não é. Reporte os dois
(ou só o de quantil, com a nota). O `MCE` é ainda mais sensível a bins pequenos; use com `N` generoso e reporte o
`N` do pior bin.

**ECE não é proper.** O ECE pode ser minimizado por um mapa que não melhora o Brier/NLL; otimize NLL/Brier no
held-out e reporte o ECE como diagnóstico. O Brier é estritamente próprio
([Brier 1950](https://doi.org/10.1175/1520-0493(1950)078%3C0001:VOFEIT%3E2.0.CO;2);
[Gneiting & Raftery, *Strictly Proper Scoring Rules, Prediction, and Estimation*, JASA 102(477):359–378,
2007](https://doi.org/10.1198/016214506000001437)). O `BrierScore` do `calib.go:198` é o Brier top-label binário
(correto/incorreto), que é o alvo da decisão; não é o Brier multiclasse. Está certo para o nosso uso, mas diga isso
no relatório.

### 6.7 Como `mhMethod.Risk` vira load-bearing

Três passos:

1. **Tabela de metadados para as `brCaps`.** `brCapMeta` (novo, em `methods.go` ou `caps.go`) com `Capability` para
   `file_op`, `git_op`, `pkg_cmd`, `systemd`, `container_op`, `proc_kill`, `undo`, `proj_cmd`, `read_cmd`,
   `ask_slot`, `say`, `tool_report`, `tool_status`, `host_report`, `host_list`, `net_status`, `test_context`.
   Sem isso, `bandOf` não vê as capacidades que importam.
2. **`bandOf(m)` lê `m.Risk` como piso.** `cbParseRisk` (`caps.go:47`) já existe; é só chamar. O declarado é um
   piso, não um teto: se o método diz `low` mas roda `bash`, a banda computada sobe; se diz `high` mas é read-only
   verificado, a banda computada pode descer, mas o piso declarado mantém `high` (conservador). A escolha "piso"
   evita que um método mal declarado fique perigoso.
3. **Teste de consistência.** `TestMethodRisk` falha se `m.Risk < banda computada`; `TestMethodVerifier` falha se um
   método não-dry-run não tem `Verify`; `TestMethodUndo` falha se um método destrutivo não tem `Undo`/checkpoint.
   Rode no `go test`; é barato e impede que o metadado volte a ser decorativo.

**Ajuste fino:** o `risk` declarado nos 49 JSONs é grosseiro. `run_tests` é `med` mas o efeito é observável e
verificado; `delete_file` é `high` mas o checkpoint cobre o `/undo`; `git_push` é `high` e não tem undo. A banda
computada identifica os casos em que o declarado está **acima** do necessário (candidatos a rebaixar) e os casos em
que está **abaixo** (o piso sobe automaticamente). Como o declarado é piso, rebaixar exige mudar o JSON e passar o
P9; não acontece por acidente. Se o declarado e o computado divergem muito, o teste aponta o método a revisar.

### 6.8 Como o `forge_gate` valida um asset de calibração

O `forge_gate.go` atual:

- `Metrics{Accuracy, FalseSuccess, Coverage, N}` (`:46-53`);
- `Gate` aceita se não há regressão de acurácia (com `gateEps=0.01`) e há melhoria de acurácia **ou** cobertura no
  held-out (`:143-180`);
- `FalseSuccess == 0` é bloqueante (`:155-157`).

Problemas para um asset de calibração:

1. **Calibração não muda acurácia.** Um mapa monotônico preserva o argmax; `CandOut.Accuracy == BaseOut.Accuracy`.
   O gate só aceitaria se `Coverage` melhorasse. Mas cobertura sozinha não é qualidade: um mapa que aumenta a
   cobertura baixando a confiança pode aumentar o risco seletivo. O gate precisa de `SelectiveRisk` e `AURC`.
2. **`gateEps=0.01` não tem base amostral.** Com 20 exemplos held-out, o erro-padrão da acurácia é ~0.1; uma queda
   de 1% é ruído. Use um IC binomial (Clopper–Pearson) ou um teste pareado.
3. **A melhoria é uma disjunção.** Um candidato pode melhorar cobertura e piorar acurácia em até 1%; o gate aceita.
   Para uma política, isso é inaceitável: precisa de dominância (não piorar nenhum dos dois, melhorar pelo menos
   um) ou de custo total.
4. **`FalseSuccess` é contagem, não taxa.** Com `n` diferente entre splits, a comparação é injusta. Use taxa + IC.
5. **Falta `n_min`.** Com 10 exemplos held-out, um flip muda a acurácia em 10%. Exija um mínimo (ex.: 40 tarefas ou
   100 exemplos) e reporte o IC; se não houver, o gate é inconclusivo, não aprovado.
6. **Falta o custo.** Um candidato que melhora a acurácia em tarefas baratas e piora nas caras passa. Use o custo
   ponderado por banda.
7. **Seed única.** O split é determinístico por `(seed, ID)` (`forge_gate.go:92-134`), o que é bom, mas a escolha
   do seed é um grau de liberdade. Pré-registre ou reporte múltiplos seeds.

Proposta de `Metrics` estendido:

```go
type Metrics struct {
    Accuracy      float64
    FalseSuccess  int
    FalseSuccRate float64 // com IC
    Coverage      float64
    SelectiveRisk float64 // erro entre os cobertos
    AURC          float64 // área sob a curva risco–cobertura
    Cost          float64 // custo esperado por pedido, com a matriz de bandas
    N             int
}
```

Regra de aceitação para um asset de calibração:

```
aceita ⇔
  CandIn/CandOut.FalseSuccess == 0
  e CandOut.AURC ≤ BaseOut.AURC − ε_AURC
  e CandOut.SelectiveRisk ≤ BaseOut.SelectiveRisk + ε_risk (na mesma cobertura)
  e CandOut.Cost ≤ BaseOut.Cost − ε_cost
  e n ≥ n_min
```

com `ε_*` derivado do bootstrap, não fixo. O `gateEps=0.01` pode continuar para a acurácia, mas como um teste
secundário; o critério primário é AURC/custo. Para um asset que só muda o mapa (não o classificador), o AURC é a
métrica certa: ele captura a melhoria da ordenação risco–cobertura sem exigir mudança de acurácia.

---

## 7. Propostas em Go para o Dell

Tudo stdlib. Impacto 1–5; esforço S/M/L; risco baixo/médio/alto. O custo 32-bit é o do runtime no Dell; o ajuste
roda no forge (amd64), como manda a filosofia.

### P1 — Núcleo da política: bandas + `Decide` + `mhMethod.Risk` load-bearing

**Mecanismo.** Novo `calib_policy.go` com `RiskBand`, `Decision`, `ActionEnv`, `QuestionCtx`, `Policy`, `Decide`,
`bandOf`, `capBand`. Nova tabela `brCapMeta` com as capacidades de `brCaps` (`methods.go:302-329`). `brExecute`
(`brain.go:261`) passa a chamar `Decide` antes de rodar o primeiro passo de um método não-read-only; se a decisão
for `DecAsk`/`DecMenu`, o método `*.ask` correspondente é escolhido (ou um `ask_slot` sintético); se for
`DecHandoff`/`DecRefuse`, a tarefa termina com o status certo. `brLevel0Min` (`brain.go:596`) vira
`pol.Tau[BandRead]` no modo correspondente (0.80 interativo, 0.90 headless); `nuMinConf` continua como gate de
"entendido", não de "agir". `brPick` passa a devolver um
`p_slot` conservador (tabela do §3.7) e `Decide` usa a cota inferior
`max(0, p_intent + p_slot − 1)`, não o `min` (que é a cota superior otimista).

**Arquivos.** `calib_policy.go` (novo, ~180 ln); `methods.go` (`brCapMeta`, `brPick`, `brChoose`); `brain.go`
(`brExecute`, `brLevel0Min`, `brHeadlessLevel0`, `brLevel0Interactive`); `caps.go` (expor `cbParseRisk`/`cbFind`);
`methods_risk_test.go` (novo).

**Custo 32-bit.** ~10 ns/decisão, zero alocação; +2–3 KB de código; tabela de 4 `float64` × 2. Irrelevante no
orçamento de 2 GB/1 CPU.

**Métrica.** Decisões antes/depois nas 75 tarefas; `false_success`; `false_blocks`; `no_llm_rate`; perguntas.
Comparar com `JAZZ_POLICY=off` (fallback para o comportamento atual).

**Impacto 5 · Esforço M · Risco médio.** Muda comportamento; sem P2/P3 usa `p` cru. Ship atrás de flag, com o eval
como gate. É a peça que dá sentido a todas as outras.

### P2 — Harness de calibração (`jazz eval calib`)

**Mecanismo.** Novo `calibrate.go` (dev/eval, não runtime) que roda os 644 exemplos com k-fold agrupado, as 100
frases cegas e as 75 tarefas; coleta `(channel, p_raw, correct, group)`; calcula ECE/MCE/Brier/NLL com bins por
quantil; ajusta Platt/temperatura por canal com LOO/k-fold; reporta a reliability table, o `GL_explained` por
partição (canal × p-bin, + missing, + oov) e a curva risco–cobertura. Escreve `results/calib-<time>.json` com
proveniência (n, seed, commit).

**Arquivos.** `calibrate.go` (novo, ~350 ln); `nlu.go` (expor `Channel`, `OOVFrac`, `Residual`); `calib.go` (novo
`QuantileBins`); `evaloffline.go` (reuso do runner). O fit pesado roda no forge; o Dell só consome o asset.

**Custo 32-bit.** Zero no runtime; +50–200 ms no eval. O k-fold com `nuTrain` é O(k·n); no Dell, evite o LOO
completo (644² treinos); use k=5.

**Métrica.** ECE/Brier por canal; `GL_explained`; fração de canais com n≥30; cobertura do fit.

**Impacto 5 · Esforço M · Risco baixo.** Aditivo; não muda decisão até P3.

### P3 — Asset `nlu/calib.json` + aplicação no `Parse`

**Mecanismo.** Gerar o asset do P2; `Parse` aplica o mapa por canal (Platt/isotônico/identidade) depois dos fatores
heurísticos; `Intent` ganha `Channel`, `PRaw`, `PCal`; fallback identidade se o asset não existe. Adicionar
`QuantileBins`, `PlattFit` com guarda `a>0`, e `IsotonicFitAnchored` com âncoras/clip. O asset é embed
(`//go:embed nlu/calib.json`), como o `intents.json`.

**Arquivos.** `nlu.go` (aplicação + campos), `nlu/calib.json` (novo), `calib.go` (quantil, guarda, âncoras),
`calibrate.go` (gerador).

**Custo 32-bit.** <1 KB de asset; ~20–60 ns/pedido; zero alocação. O `math.Log`/`Exp` do Platt é ~50 ns no i686;
o `Parse` já gasta ~ms. Overhead <1%.

**Métrica.** ECE↓; `false_blocks`↓ com `no_llm_rate` constante; `false_success=0` preservado; decisões antes/depois
nas 75 tarefas.

**Impacto 4 · Esforço M · Risco médio.** Dados pequenos; mitigar com LOO, clip, shrinkage e fallback. Não mexer no
argmax.

### P4 — Calibração do resolvedor (mapa separado)

**Mecanismo.** Extrair features de `niEntity` (`Dist`, `Exact`, margem top1−top2, `#cands`, extensão, hint,
recência, tamanho relativo) e ajustar um Platt/logístico pequeno sobre um corpus de resoluções rotuladas (typos
sintéticos no teclado + logs reais). `brPick` passa a exigir `p_resolve ≥ τ(BandRead)` para agir sem perguntar;
`niTop` usa uma margem em probabilidade, não `fzSlip`. O mapa é separado do mapa de intenção: o alvo é
`P(candidato certo)`, não `P(intenção certa)`.

**Arquivos.** `nameindex.go` (expor features), `methods.go` (`brPick`, `brChoose`), `calibrate.go` (fit + asset
`nlu/resolve_calib.json`).

**Custo 32-bit.** ~8 features × 10 candidatos = 80 multiplicações; ~1 µs; sem alocação se pré-alocado.

**Métrica.** Perguntas por resolução↓ sem pick errado; `false_success=0`; `cmds/task` estável.

**Impacto 4 · Esforço M · Risco médio-alto.** Exige corpus honesto; typos sintéticos são proxy, não a distribuição
real. Sem corpus, use a tabela conservadora do §3.7 (impacto 3, esforço S).

### P5 — Métricas de decisão no eval

**Mecanismo.** Estender `eoSummary` (`evaloffline.go:91-101`) com `SelectiveRisk`, `Coverage`, `AURC`, `Cost`,
`RiskAtCoverage`, `NearN`, `BootstrapCI`; calcular por banda e por canal; bootstrap por grupo (tarefa/família);
reportar `n_near` (quantos exemplos perto do `τ`). O `false_success` continua bloqueante.

**Arquivos.** `evaloffline.go`; `results/`.

**Custo 32-bit.** Eval-only; bootstrap 1000×75 ≈ 50 ms; `cpUpper` ~1 ms para a grade.

**Métrica.** As métricas que decidem; habilita P6/P7.

**Impacto 4 · Esforço S–M · Risco baixo.**

### P6 — `forge_gate` para assets de calibração

**Mecanismo.** `Metrics` ganha `SelectiveRisk`, `AURC`, `Cost`, `FalseSuccRate`, `N`; `Gate` aceita um asset de
calibração por dominância de AURC/custo (não por acurácia), com `n_min`, IC bootstrap e `false_success=0`.
`gateEps` fixo vira um teste secundário; o primário é o custo/AURC. Reportar o `GateReport` com os números e o IC.

**Arquivos.** `forge_gate.go`, `forge_gate_test.go`.

**Custo 32-bit.** Eval-only.

**Métrica.** Falsos aceites/rejeites em candidatos sintéticos; ΔAURC/ΔCusto com IC.

**Impacto 4 · Esforço S · Risco médio.** Muda a semântica do gate; versione (`gate_version`) e mantenha o gate
antigo para assets que não são de calibração.

### P7 — Ledger de produção amostrado + CUSUM

**Mecanismo.** `Signal` (`signals.go:26-40`) ganha `PCal`, `Band`, `Decision`, `Outcome`, `Verified`. Registrar uma
linha por decisão (ou 1 em cada 10, configurável) com `Text` redigido (`redactSecrets`), rotação em 10 MB. Novo
`jazz eval drift` calcula, por banda, a taxa de `false_success` com um CUSUM
([Page, *Continuous Inspection Schemes*, Biometrika 41(1–2):100–115, 1954](https://doi.org/10.1093/biomet/41.1-2.100))
ou SPRT ([Wald 1945](https://doi.org/10.1214/aoms/1177731118)). Alerta quando o CUSUM/SPRT acusa aumento da taxa
acima de `α_b`.

**Arquivos.** `signals.go`, novo `drift.go` (ou `evaloffline.go`), config.

**Custo 32-bit.** Uma linha JSON (~200 B) por decisão; append O(1) com flock (~50 µs); 10k decisões ≈ 2 MB; rotação.
O CUSUM é O(n) na leitura, eval-only.

**Métrica.** Atraso de detecção para um shift simulado; taxa de falso alarme; cobertura do denominador.

**Impacto 3 · Esforço M · Risco médio.** Privacidade: o `Text` já é logado; redigir antes. O ledger é
auto-selecionado (só misses/correções); sem o denominador amostrado, não serve para calibrar, só para monitorar.

### P8 — Massa top-k para o menu

**Mecanismo.** `classify` (`nlu.go:1429`) hoje devolve `(classe, p)` condicionado a `among`; expor a distribuição
completa (ou a massa top-k) sem mudar o argmax. `brChoose`/`olOffer` exigem `TopMass ≥ 0.90` e `k ≤ 5`; se não,
perguntar um slot ou handoff. Para o resolvedor, `niTop` devolve a massa normalizada dos candidatos.

**Arquivos.** `nlu.go` (`classify`/`Parse`), `methods.go` (`brChoose`), `offers.go` (`olOffer`).

**Custo 32-bit.** O softmax já percorre as 50 classes; somar a massa top-k é O(50), ~100 ns; zero alocação se
reusar um buffer.

**Métrica.** Taxa de pick errado no menu↓; perguntas não↑; `false_success=0`.

**Impacto 3 · Esforço S–M · Risco médio.** Muda a semântica de `p`; recalibrar depois (P3).

### P9 — Teste de consistência do risco (pode entrar no P1)

**Mecanismo.** `TestMethodRisk`/`TestMethodVerifier`/`TestMethodUndo` em `methods_risk_test.go`: falham se o
`risk` declarado é menor que a banda computada, se um método não-dry-run não tem `Verify`, ou se um método
destrutivo não tem `Undo`. Roda no `go test`.

**Custo 32-bit.** Test-only.

**Métrica.** Número de divergências; impede que o metadado volte a ser decorativo.

**Impacto 3 · Esforço S · Risco baixo.**

### P10 — Ferramenta de sensibilidade do custo

**Mecanismo.** `jazz eval policy --cost-grid` roda a política sobre uma grade de razões `r ∈ {2,5,10,20,50,100}`
e reporta cobertura, risco, `false_success`, `false_blocks` e a fração de decisões que viram em relação ao default.
Se a fração é alta, o custo não é identificável com os dados; escolha o extremo conservador.

**Custo 32-bit.** Eval-only.

**Métrica.** % de flips; envelope conservador.

**Impacto 3 · Esforço S · Risco baixo.**

---

## 8. O que NÃO fazer

1. **Não usar entropia como variável de decisão.** Em multiclasse, a ordem não coincide com o máximo posterior
   (§1.2). Use `max p` calibrado. Entropia serve para escolher a pergunta, não para decidir agir.
2. **Não usar um limiar global.** `BandRead` e `BandIrreversible` não podem ter o mesmo `τ`. A regra de Chow com
   custos diferentes exige limiares diferentes; um limiar único ou over-age no destrutivo ou sub-age no read-only.
3. **Não calibrar e escolher `τ` no mesmo conjunto.** O mínimo empírico é otimista (winner's curse); use LOO/k-fold
   aninhado, SGR/LTT, faixa de guarda (§4.2–4.5).
4. **Não ajustar nas 75 tarefas nem nas 100 frases cegas.** Elas são o teste de aceitação; se virarem treino, o
   eval perde o sentido.
5. **Não usar isotônico com <100–200 pontos por canal.** PAV com 16 nós e 50 pontos é ruído monotônico; use
   Platt/temperatura e shrinkage (§6.2).
6. **Não multiplicar `p_intent × p_slot × p_method` sem checar correlação.** Use a cota inferior
   `max(0, p_intent + p_slot − 1)` ou calibre o fim-a-fim; as cotas de Fréchet–Hoeffding dão o intervalo (§5.5).
7. **Não tratar `Confidence` como `P(sucesso)`.** É `P(intenção)` no melhor caso; o slot e o método podem derrubar
   o sucesso (§1.4).
8. **Não auto-agir em ação irreversível, por maior que seja `p`.** O score bruto capa em 0.97; a regra dura é
   separada, não um `τ` alto (§3.1).
9. **Não oferecer menu sem massa top-k.** Um menu de 5 opções que não contém a resposta é pior que uma pergunta
   livre; a condição de cobertura é obrigatória (§3.5).
10. **Não usar "0 false_success em 75 tarefas" como prova de <1%.** O limite superior é ~4% (§4.7); para 1%,
    ~300 tarefas.
11. **Não usar acurácia como gate de um asset de calibração.** Um mapa monotônico não muda a acurácia; use
    AURC/custo/risco seletivo (§6.8).
12. **Não adicionar features pós-decisão ao mapa.** `correct`, "o método falhou", "o usuário corrigiu", "o modelo
    foi chamado" são vazamento; a decisão não os tem no momento de decidir (§5.3).
13. **Não usar o ledger de sinais como conjunto de calibração não enviesado.** Ele registra misses/correções, não
    o denominador; sem amostragem, é um banco de casos, não uma amostra (§7 P7).
14. **Não usar a confiança auto-reportada do modelo.** O LLM não é calibrado por padrão; e não pode estar no
    caminho crítico de runtime.
15. **Não confundir o custo do efeito colateral com o custo do claim falso.** Um verificador perfeito pode zerar
    `false_success` depois de um `git push --force` errado; a política precisa custar os dois separadamente (§2.4,
    §3.1).
16. **Não ignorar a exchangeability.** Todos os bounds (SGR, LTT, conformal) assumem i.i.d./exchangeable; os 644
    exemplos curados e os pedidos reais não são. A garantia é condicional; monitore a deriva (§4.4).
17. **Não otimizar ECE diretamente.** ECE não é proper; otimize NLL/Brier no held-out e reporte o ECE (§6.6).
18. **Não usar bins de largura fixa com `n < 10^3`.** Use bins por quantil; senão o ECE é dominado por um bin
    (§6.6).
19. **Não tratar `false_blocks` como `false_success`.** O primeiro é usabilidade; o segundo é segurança. A ordem é
    lexicográfica: `false_success=0` primeiro, depois `false_blocks`/cobertura (§2.4).
20. **Não deixar `mhMethod.Risk` e `Capability.Risk` como decoração.** Se o metadado não é lido, ele mente; o teste
    de consistência do P9 é o antídoto (§6.7).

---

## 9. O que não tem resposta boa

1. **A matriz de custo verdadeira.** `C_erro` por banda e o custo de interromper o usuário são decisões de produto
   e ética, não quantidades estimáveis de 75 tarefas. A política pode derivar `τ` de uma matriz declarada; não pode
   descobrir a matriz. A melhor prática é declarar, testar a sensibilidade (§4.9) e manter o irreversível
   human-gated.
2. **A distribuição real dos pedidos.** Os 644 exemplos são curados e pt-BR; os pedidos reais podem ser outros. A
   calibração neles é uma priori; a transferência sob shift não é garantida
   ([Ovadia et al., *Can You Trust Your Model's Uncertainty?*, arXiv:1906.02530, 2019](https://arxiv.org/abs/1906.02530)).
   Até o ledger ter volume e um esquema de amostragem, não há como medir a taxa real.
3. **`P(sucesso)` fim-a-fim com 644 exemplos.** Por intenção, são 12,9 exemplos; por método, menos. A cota
   inferior `max(0, p_intent + p_slot − 1)` é conservadora, mas pode ser excessivamente conservadora; o `min` é a
   cota superior (otimista) e o produto é um ponto sob uma hipótese não testada. Não há dados para decidir qual é o
   certo.
4. **A calibração real do resolvedor.** Exige um corpus de resoluções rotuladas; typos sintéticos são um proxy. O
   P4 é a proposta, mas o corpus não existe hoje.
5. **A massa top-k do menu.** A NLU atual condiciona `p` ao conjunto de empatados; não há distribuição global. O P8
   expõe, mas muda a semântica de `p` e exige recalibrar. Até lá, o menu é heurístico.
6. **Certificar 1% de `false_success` com 75 tarefas.** Impossível; o limite é ~4%. Precisa de ~300 tarefas ou de
   monitoramento em produção. O eval atual é um teste de fumaça.
7. **Selecionar features de grouping loss com 644 exemplos.** O `GL_explained` é um limitante inferior ruidoso; a
   seleção de features com n pequeno overfitta. A recomendação (canal, missing, oov; depois parar) é uma escolha
   conservadora, não uma descoberta.
8. **O custo de interrupção por usuário/contexto.** Varia por pessoa, hora e tarefa; uma política única é uma
   média. Personalizar exigiria dados que não temos e levantaria privacidade.
9. **`p_model` para o handoff.** A confiabilidade do modelo no pedido específico não é medida; usar uma priori
   conservadora é o melhor que dá para fazer sem um eval caro.
10. **A garantia sob drift.** Os bounds de SGR/LTT/conformal são condicionais à exchangeability; sob drift, só
    valem versões ponderadas (Barber et al. 2023) ou monitoramento. Não há garantia gratuita.

---

## 10. Referências

- Chow, C. K. *On optimum recognition error and reject tradeoff*. IEEE Trans. Inf. Theory 16(1):41–46, 1970.
  [doi:10.1109/TIT.1970.1054406](https://doi.org/10.1109/TIT.1970.1054406)
- El-Yaniv, R. & Wiener, Y. *On the Foundations of Noise-free Selective Classification*. JMLR 11:1605–1641, 2010.
  [jmlr.org/papers/v11/el-yaniv10a](https://jmlr.org/papers/v11/el-yaniv10a.html)
- Geifman, Y. & El-Yaniv, R. *Selective Classification for Deep Neural Networks*. NeurIPS 2017.
  [arXiv:1705.08500](https://arxiv.org/abs/1705.08500)
- Geifman, Y. & El-Yaniv, R. *SelectiveNet: A Deep Neural Network with an Integrated Reject Option*. ICML 2019.
  [arXiv:1901.09192](https://arxiv.org/abs/1901.09192)
- Hendrycks, D. & Gimpel, K. *A Baseline for Detecting Misclassified and Out-of-Distribution Examples in Neural
  Networks*. ICLR 2017. [arXiv:1610.02136](https://arxiv.org/abs/1610.02136)
- Hendrickx, K., Perini, L., Van der Plas, D., Meert, W. & Davis, J. *Machine learning with a reject option: a
  survey*. Machine Learning 113:3073–3110, 2024.
  [doi:10.1007/s10994-024-06534-x](https://doi.org/10.1007/s10994-024-06534-x)
- Perez-Lebel, A., Le Morvan, M. & Varoquaux, G. *Beyond calibration: estimating the grouping loss of modern
  neural networks*. ICLR 2023. [arXiv:2210.16315](https://arxiv.org/abs/2210.16315)
- Angelopoulos, A. N., Bates, S., Candès, E. J., Jordan, M. I. & Lei, L. *Learn then Test: Calibrating Predictive
  Algorithms to Achieve Risk Control*. 2022. [arXiv:2110.01052](https://arxiv.org/abs/2110.01052)
- Angelopoulos, A. N., Bates, S., Fisch, A., Lei, L. & Schuster, T. *Conformal Risk Control*. 2022.
  [arXiv:2208.02814](https://arxiv.org/abs/2208.02814)
- Vovk, V., Gammerman, A. & Shafer, G. *Algorithmic Learning in a Random World*. Springer, 2005.
  [doi:10.1007/b106715](https://doi.org/10.1007/b106715)
- Lei, J., G'Sell, M., Rinaldo, A., Tibshirani, R. J. & Wasserman, L. *Distribution-Free Predictive Inference for
  Regression*. JASA 113(523):1094–1111, 2018.
  [doi:10.1080/01621459.2017.1307116](https://doi.org/10.1080/01621459.2017.1307116)
- Barber, R. F., Candès, E. J., Ramdas, A. & Tibshirani, R. J. *Conformal prediction beyond exchangeability*.
  Annals of Statistics 51(2):816–845, 2023. [doi:10.1214/23-AOS2276](https://doi.org/10.1214/23-AOS2276)
- Gascuel, O. & Caraux, G. *Distribution-free performance bounds with the resubstitution error estimate*. Pattern
  Recognition Letters 13(11):757–764, 1992.
  [doi:10.1016/0167-8655(92)90125-J](https://doi.org/10.1016/0167-8655(92)90125-J)
- Clopper, C. J. & Pearson, E. S. *The use of confidence or fiducial limits illustrated in the case of the
  binomial*. Biometrika 26(4):404–413, 1934. [doi:10.1093/biomet/26.4.404](https://doi.org/10.1093/biomet/26.4.404)
- Wilson, E. B. *Probable inference, the law of succession, and statistical inference*. JASA 22(158):209–212,
  1927. [doi:10.1080/01621459.1927.10502953](https://doi.org/10.1080/01621459.1927.10502953)
- Hanley, J. A. & Lippman-Hand, A. *If Nothing Goes Wrong, Is Everything All Right? Interpreting Zero Numerators*.
  JAMA 249(13):1743–1745, 1983.
  [doi:10.1001/jama.1983.03330370053031](https://doi.org/10.1001/jama.1983.03330370053031)
- Cawley, G. C. & Talbot, N. L. C. *On Over-fitting in Model Selection and Subsequent Selection Bias in
  Performance Evaluation*. JMLR 11:2079–2107, 2010.
  [jmlr.org/papers/v11/cawley10a](https://www.jmlr.org/papers/v11/cawley10a.html)
- Varma, S. & Simon, R. *Bias in error estimation when using cross-validation for model selection*. BMC
  Bioinformatics 7:91, 2006. [doi:10.1186/1471-2105-7-91](https://doi.org/10.1186/1471-2105-7-91)
- Arlot, S. & Celisse, A. *A survey of cross-validation procedures for model selection*. Statistics Surveys
  4:40–79, 2010. [doi:10.1214/09-SS054](https://doi.org/10.1214/09-SS054)
- Efron, B. & Tibshirani, R. J. *An Introduction to the Bootstrap*. Chapman & Hall/CRC, 1993.
  [doi:10.1201/9780429246593](https://doi.org/10.1201/9780429246593)
- Efron, B. & Morris, C. *Data Analysis Using Stein's Estimator and Its Generalizations*. JASA 70(350):311–319,
  1975. [doi:10.1080/01621459.1975.10479864](https://doi.org/10.1080/01621459.1975.10479864)
- Gelman, A., Carlin, J. B., Stern, H. S., Dunson, D. B., Vehtari, A. & Rubin, D. B. *Bayesian Data Analysis*.
  3ª ed., CRC Press, 2013. [doi:10.1201/b16018](https://doi.org/10.1201/b16018)
- Platt, J. *Probabilistic Outputs for Support Vector Machines and Comparisons to Regularized Likelihood
  Methods*. 1999. [citeseer:10.1.1.41.1639](http://citeseerx.ist.psu.edu/viewdoc/summary?doi=10.1.1.41.1639)
- Zadrozny, B. & Elkan, C. *Transforming classifier scores into accurate multiclass probability estimates*. KDD
  2002. [doi:10.1145/775047.775151](https://doi.org/10.1145/775047.775151)
- Ayer, M., Brunk, H. D., Ewing, G. M., Reid, W. T. & Silverman, E. *An Empirical Distribution Function for
  Sampling with Partial Ordering*. Annals of Mathematical Statistics 26(4):641–647, 1955.
  [doi:10.1214/aoms/1177728423](https://doi.org/10.1214/aoms/1177728423)
- Niculescu-Mizil, A. & Caruana, R. *Predicting Good Probabilities with Supervised Learning*. ICML 2005.
  [doi:10.1145/1102351.1102430](https://doi.org/10.1145/1102351.1102430)
- Kull, M., Silva Filho, T. & Flach, P. *Beta calibration: a well-founded and easily implemented improvement on
  logistic calibration for binary classifiers*. AISTATS 2017.
  [PMLR v54](https://proceedings.mlr.press/v54/kull17a.html)
- Guo, C., Pleiss, G., Sun, Y. & Weinberger, K. Q. *On Calibration of Modern Neural Networks*. ICML 2017.
  [arXiv:1706.04599](https://arxiv.org/abs/1706.04599)
- Naeini, M. P., Cooper, G. F. & Hauskrecht, M. *Obtaining Well Calibrated Probabilities Using Bayesian Binning*.
  AAAI 2015.
- Vaicenavicius, J., Widmann, D., Andersson, C., Lindsten, F., Roll, J. & Schön, T. B. *Evaluating model
  calibration in classification*. AISTATS 2019. [PMLR v89](https://proceedings.mlr.press/v89/vaicenavicius19a.html)
- Brier, G. W. *Verification of Forecasts Expressed in Terms of Probability*. Monthly Weather Review 78(1):1–3,
  1950. [doi:10.1175/1520-0493(1950)078%3C0001:VOFEIT%3E2.0.CO;2](https://doi.org/10.1175/1520-0493(1950)078%3C0001:VOFEIT%3E2.0.CO;2)
- Gneiting, T. & Raftery, A. E. *Strictly Proper Scoring Rules, Prediction, and Estimation*. JASA
  102(477):359–378, 2007. [doi:10.1198/016214506000001437](https://doi.org/10.1198/016214506000001437)
- Elkan, C. *The Foundations of Cost-Sensitive Learning*. IJCAI 2001.
  [ijcai.org/proceedings/2001/133](https://ijcai.org/proceedings/2001/133)
- Domingos, P. & Pazzani, M. *On the Optimality of the Simple Bayesian Classifier under Zero-One Loss*. Machine
  Learning 29:103–130, 1997. [doi:10.1023/A:1007413511361](https://doi.org/10.1023/A:1007413511361)
- Ng, A. Y. & Jordan, M. I. *On Discriminative vs. Generative Classifiers: A comparison of logistic regression
  and naive Bayes*. NeurIPS 2001.
- Krichevsky, R. E. & Trofimov, V. K. *The performance of universal encoding*. IEEE Trans. Inf. Theory
  27(2):199–207, 1981. [doi:10.1109/TIT.1981.1056331](https://doi.org/10.1109/TIT.1981.1056331)
- Kapoor, S. & Narayanan, A. *Leakage and the Reproducibility Crisis in ML-based Science*. 2023.
  [arXiv:2207.07048](https://arxiv.org/abs/2207.07048)
- Ovadia, Y. et al. *Can You Trust Your Model's Uncertainty? Evaluating Predictive Uncertainty Under Dataset
  Shift*. NeurIPS 2019. [arXiv:1906.02530](https://arxiv.org/abs/1906.02530)
- Page, E. S. *Continuous Inspection Schemes*. Biometrika 41(1–2):100–115, 1954.
  [doi:10.1093/biomet/41.1-2.100](https://doi.org/10.1093/biomet/41.1-2.100)
- Wald, A. *Sequential Tests of Statistical Hypotheses*. Annals of Mathematical Statistics 16(2):117–186, 1945.
  [doi:10.1214/aoms/1177731118](https://doi.org/10.1214/aoms/1177731118)
- Horvitz, E. *Principles of Mixed-Initiative User Interfaces*. CHI 1999.
  [doi:10.1145/302979.303030](https://doi.org/10.1145/302979.303030)
- Nelsen, R. B. *An Introduction to Copulas*. 2ª ed., Springer, 2006.
  [doi:10.1007/0-387-28678-0](https://doi.org/10.1007/0-387-28678-0)
- Hastie, T., Tibshirani, R. & Friedman, J. *The Elements of Statistical Learning*. 2ª ed., Springer, 2009.
  [doi:10.1007/978-0-387-84858-7](https://doi.org/10.1007/978-0-387-84858-7)
- Berger, J. O. *Statistical Decision Theory and Bayesian Analysis*. 2ª ed., Springer, 1985.
  [doi:10.1007/978-1-4757-4286-2](https://doi.org/10.1007/978-1-4757-4286-2)
- Cover, T. M. & Thomas, J. A. *Elements of Information Theory*. 2ª ed., Wiley, 2006.
  [doi:10.1002/047174882X](https://doi.org/10.1002/047174882X)
