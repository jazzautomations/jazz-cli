# Baixo nível para o modo offline: assembly, C, sistema operacional e internals do Go

Autor: Fable (pesquisa), 26/10/2026. Insumos: `docs/design/offline-brain.md`, `docs/research/offline-brain-research.md`,
`docs/research/harness-first-offline.md` e o código do `main` em a54368e. Escopo: **só itens acionáveis e mensuráveis
neste repositório**, para a máquina de referência. Nada aqui é "boa prática genérica"; cada item diz o mecanismo,
onde mexer, o ganho esperado e como medir no Dell.

Máquina de referência: Dell Latitude D610, Pentium M (Dothan) 1,7 GHz, **1 núcleo, sem HT**, SSE2 sim, SSE3/SSSE3/
SSE4/POPCNT/AES-NI **não**, L1d 32 KB, L2 2 MB, linha de cache 64 B, FSB 400 MHz; 2 GB de RAM; HDD 2,5" (busca
aleatória na casa de 10 ms); antiX, kernel 5.10, userland 32 bits (`linux/386`), bubblewrap. A especificação do
735 (2 MB de L2, 400 MHz de FSB) está na página da Intel, SKU 27588 [intel-735].

Convenção: "**medido**" = rodei hoje no Mac (Apple M4, darwin/arm64, Go 1.27.2) com o código do `main`; "**estimado
no Dell**" = medido × fator 20–40 (desempenho de um núcleo, inteiro, M4 vs Pentium M de 2005). O fator é palpite
até a primeira rodada do protocolo da §1, e todo número final tem que vir de uma rodada registrada em `results/`
(regra da spec, §9).

---

## 0. Achados de hoje (medidos, antes de qualquer mudança)

| # | Achado | Número (M4) | Onde |
|---|---|---|---|
| A1 | `init` do pacote `main` aloca **9,45 MB em 58 880 alocações** e leva ~5 ms antes do `main()` rodar | `GODEBUG=inittrace=1 jazz version` → `init main @1.0 ms, 5.0 ms clock, 9454080 bytes, 58880 allocs` | 433 `regexp.MustCompile` em `var` de pacote (contados com `go/ast`) |
| A2 | Só as 433 regexps somam **8,98 MB e 55 939 alocações** (95 % do A1) | programa de medição com `go/ast` + `runtime.MemStats` | `safety.go` (40), `buildtarget.go` (38), `redact.go` (38), `docs.go` (31), `guard_honest.go` (30), … |
| A3 | `jazz version` dispara **4 ciclos de GC** antes de imprimir a versão | `GODEBUG=gctrace=1`: `gc 1 @0.001s … 4 MB goal` até `gc 4 @0.005s` | o alvo mínimo de heap é 4 MiB [gc-guide]; `pfTuneRuntime` (ui_width.go) só roda **depois** do `init` |
| A4 | `fzDist` faz **3 alocações por comparação** (`[]rune`×2 implícito + 3 `make([]int)`) e custa 6,1 µs para chaves de 16–17 letras | `BenchmarkFzDist`: 6103 ns/op, 432 B/op, 3 allocs/op | fuzzy.go |
| A5 | `fzNeighbour` (2 buscas em `map[rune][2]int` por substituição) é **52 % do tempo** de `niIndex.resolve` | `pprof -top`: `fzNeighbour` 52 % cum, `mapaccess2_fast32` 25 % | fuzzy.go, nameindex.go |
| A6 | Protótipo: `fzDist` só-ASCII com linhas em array na pilha e tabela de vizinhos em bitset: **408 ns, 0 alocações** (15×), resultado idêntico ao original em 144 pares | `BenchmarkZZProtoDist` (descartado após medir) | fuzzy.go |
| A7 | `resolve` de 1 token num repo sintético de 5 000 arquivos: **26 ms, 15 047 alocações** (3 por arquivo) | `BenchmarkNiResolve5k` | nameindex.go |
| A8 | `niBuild` de 5 000 arquivos (cache quente): **27,5 ms, 150 694 alocações, 7,9 MB**; vivo depois: 2,5 MB em 40 039 objetos | `BenchmarkNiBuild5k` + `MemStats` | nameindex.go |
| A9 | `fzIntent("procrua")`: **52 µs, 259 alocações** (varre o mapa `fzVerbs` inteiro com `fzDist`) | `BenchmarkFzIntent` | fuzzy.go |
| A10 | `fpSet.Match("roda os tesets")` (caminho com typo): **46,5 µs, 327 alocações** | `BenchmarkFpMatchTypo` | intents.go |
| A11 | Binário `linux/386` com `-s -w`: **13,2 MB** (sem: 18,8 MB). `main.init` sozinho tem 56 KB de código | `go tool nm -size -sort size` | release.sh já usa `-s -w -trimpath` |
| A12 | `jazz -c` / `--resume-last-in` / `/sessions` fazem `json.Unmarshal` de **todas as linhas de todas as sessões** da pasta, com buffer de 1 MB por arquivo | leitura do código | `sessListDir` (session.go) |

Estimado no Dell: A1+A3 = 100–200 ms de CPU **em todo comando**, inclusive `jazz version` e `jazz -p`; A7 = 0,5–1 s
por token com nome num repo de 5k arquivos; A9/A10 = 1–2 ms por pedido (ok, mas é desperdício puro de alocação).

---

## 1. Protocolo de medição (linha de base primeiro, sempre no Dell)

Ferramentas presentes no antiX sem instalar nada: `/usr/bin/time` (GNU time, pacote `time`; checar com
`/usr/bin/time -V`), `GODEBUG`, `go tool pprof` (no Mac, com o perfil copiado), `/proc`. `perf` provavelmente não
existe para o kernel do antiX; `hyperfine` só se estiver no repositório; o laço de shell abaixo substitui.

```sh
# 0. binário de release (mesmas flags do scripts/release.sh), copiado para o Dell pelo dono
CGO_ENABLED=0 GOOS=linux GOARCH=386 go build -trimpath -ldflags='-s -w -buildid=' -o jazz-386 .
# testes/benchmarks compilados para rodar no Dell sem Go instalado lá
GOOS=linux GOARCH=386 go test -c -o jazz-386.test .

# 1. partida a frio (página do binário fora do page cache). Precisa de root; 3 = page cache + dentries/inodes
sync; echo 3 | sudo tee /proc/sys/vm/drop_caches >/dev/null
/usr/bin/time -v ./jazz-386 version 2>&1 | egrep 'Elapsed|Maximum resident|Major|Minor|Voluntary|Involuntary'
# 2. partida a quente: 50 rodadas, mediana
for i in $(seq 50); do /usr/bin/time -f '%e %M' ./jazz-386 version 2>&1 >/dev/null | tail -1; done | sort -n | awk '{a[NR]=$0} END{print "p50",a[int(NR/2)],"p95",a[int(NR*0.95)]}'
# 3. custo do init e do GC antes do main
GODEBUG=inittrace=1 ./jazz-386 version 2>&1 | grep 'init main'
GODEBUG=gctrace=1   ./jazz-386 version 2>&1 | grep -c '^gc '
# 4. caminho rápido offline (sem rede, sem modelo): latência e RSS de pico
cd ~/repo-5k && JAZZ_OFFLINE=1 /usr/bin/time -v ~/jazz-386 --offline -p 'git status' 2>&1 | egrep 'Elapsed|Maximum resident|Major'
# 5. índice de nomes num repo de 5k arquivos (frio e quente) e microbenchmarks no próprio Dell
sync; echo 3 | sudo tee /proc/sys/vm/drop_caches >/dev/null
./jazz-386.test -test.run XXX -test.bench 'NiBuild|NiResolve|FzDist|FzIntent|FpMatch' -test.benchmem -test.count 5 | tee results/bench-dell-$(date +%F).txt
# 6. perfil de CPU de uma sessão real (ver §9) e trace de agendamento/GC
JAZZ_CPUPROFILE=/tmp/jazz.pprof ./jazz-386 ...   # a implementar: 10 linhas em main.go (pprof.StartCPUProfile)
JAZZ_TRACE=/tmp/jazz.trace ./jazz-386 ...        # runtime/trace; abrir com `go tool trace` no Mac
```

Regras: uma ferramenta de diagnóstico por vez (perfis interferem entre si [diagnostics]); mesma rodada com e sem a
mudança, 5+ repetições, reportar p50/p95 e não a média; registrar `uname -r`, `free -m` e
`cat /sys/block/sda/queue/read_ahead_kb`. Comparação com `benchstat` no Mac sobre os `.txt` do Dell. Repo de 5k
arquivos reprodutível: script em `evals/` que gera a árvore (o gerador que usei para A7/A8: 50 pacotes × 7 subpastas
× arquivos `modulo_calculo_NNNN.go`), mais um repo real (por exemplo um checkout fixo do próprio jazz-cli × 20).

Métricas da linha de base (0.8.1) a registrar antes de mexer: (a) `jazz version` frio/quente; (b) `init main`
bytes/allocs e nº de GCs; (c) `jazz --offline -p` em 5 pedidos do dataset (git status, rodar testes, abrir arquivo
com typo, procurar símbolo com typo, "o que mudou"); (d) RSS de pico de (c); (e) `niBuild` frio/quente em 5k;
(f) `jazz -c` com 50 e 300 sessões na pasta.

---

## 2. Go em `GOARCH=386`: o que muda de verdade

**2.1 `GO386=sse2` é o certo; `softfloat` nunca.** `GO386` só aceita `sse2` (padrão) e `softfloat` (doc do `go`
[go-env]; o 387 saiu no Go 1.16). O Pentium M tem SSE2, então ponto flutuante vira `ADDSD/MULSD` escalares;
`softfloat` troca cada operação por chamada de função (dezenas de ciclos). O release já sai com `GO386=sse2`
(confirmado com `go version -m`). Ação: nenhuma, só um teste no `release.sh` que falhe se `GO386` vier setado.
Consequência de projeto: no naive Bayes e no ranking, inteiro continua melhor que `float64` (§7), não por falta de
FPU, mas porque `int32` cabe em registrador e `float64` num `log` é chamada de biblioteca.

**2.2 Ponteiro de 4 bytes: estruturas ficam menores, mas `int64` custa dois registradores.** Em 386, `string` = 8 B,
slice = 12 B, `map`/ponteiro = 4 B, `int` = 4 B. Oito registradores de uso geral (com ESP/EBP, sobram ~6): laços com
muitas variáveis vivas derramam para a pilha. `int64`/`uint64` viram pares de registradores e `ADD/ADC`; contadores
em `time.Duration` e `UnixMilli()` são 64 bits por definição, então **não usar `int64` em laço quente** (ex.: o
`fzDist` e o NB devem usar `int32`/`int`). `math/bits.OnesCount32` não tem `POPCNT` no Dothan (SSE4.2): sai pela
versão em tabela/SWAR — ainda barato, mas preferir `uint32` a `uint64` nas máscaras.

**2.3 `bytes.IndexByte` em 386 é `REPNE SCASB`, não SIMD.** Lendo a fonte do Go 1.27
(`src/internal/bytealg/indexbyte_386.s`): `CLD; REPNE; SCASB`. Em amd64 o mesmo arquivo usa SSE2/AVX2 (16–32 B por
iteração). Agner Fog: "REP LOADS, REP SCAS, and REP CMPS take more time per iteration than simple loops" e o
truque SWAR de Mycroft `(w - 0x01010101) & ~w & 0x80808080` "is faster than using REPNE SCASB" [agner-asm §13,
§16.9]. Também em 386: `bytealg.MaxLen = 0` (não há `Index` em assembly; `strings.Index` usa `IndexByte` + Rabin-Karp),
enquanto `Equal`/`Compare` usam SSE2 (`PCMPEQB`, `MOVOU`). Implicação: **todo `strings.Index`, `strings.Contains`,
`bytes.IndexByte`, `bufio.Scanner` (procura `\n`) e prefixo literal de regexp no Dell anda ~1 byte por iteração
de microcódigo.** Ver §10 sobre quando (raramente) vale assembly por causa disso.

**2.4 Hash de mapa sem AES-NI.** O runtime só instala o hash AES se `HasAES && HasSSSE3 && HasSSE41`
(`src/internal/runtime/maps/runtime_alg.go`); o Dothan não tem nenhum dos três, então todo `map[string]…` usa o hash
em software de 32 bits. Mapa no laço quente é o pior caso possível no Dell — é exatamente o A5 (`fzPos` é
`map[rune][2]int` consultado 2× por célula da matriz de distância). Regra: dentro de laço por arquivo/por célula,
**array indexado por byte**, nunca mapa.

**2.5 Pilha de goroutine começa em 2 KB e cresce por cópia** (`stackMin = 2048`, `runtime/stack.go`; o runtime
ajusta o tamanho inicial pela média observada). Cada crescimento copia a pilha inteira e reajusta ponteiros. Arrays
grandes na pilha (§6) são bons até alguns KB; acima disso o compilador manda para o heap de qualquer forma. O
protótipo A6 usa 3 × `[65]int32` = 780 B: cabe.

---

## 3. GC para um CLI de vida curta num núcleo só

Mecanismo: o GC concorrente marca usando ~25 % de CPU na fase de marcação [gc-guide]; com 1 CPU 100 % ocupada,
**custo de CPU do GC = tempo de parede** (o próprio guia: "If the program runs on 1 CPU and fully utilizes its
resources, then these are equivalent"). O alvo mínimo é 4 MiB ("the Go GC has a minimum total heap size of 4 MiB").
Por isso A3: 9,4 MB de lixo de `init` = 4 GCs inúteis antes do `main`.

**3.1 Ajustar o GC antes do `init` do `main`, não dentro do `main()`.** Hoje `pfTuneRuntime()` (ui_width.go) roda na
primeira linha do `main()`, depois de todo o `init`. A ordem de inicialização do Go garante que pacotes importados
inicializam antes do `main`. Ação: pacote `jazzcli/internal/early` com um único `func init()` que, se
`runtime.NumCPU()==1 && os.Getenv("GOGC")==""`, chama `debug.SetGCPercent(400)` e `debug.SetMemoryLimit(384<<20)`;
importado com `_` em main.go. Ganho: os 4 GCs do A3 caem para 0–1 já hoje, antes até de consertar as regexps.
Medida: `GODEBUG=gctrace=1 jazz-386 version | grep -c '^gc '` (meta: 0).

**3.2 `GOMEMLIMIT` + `GOGC` alto, sem ballast.** Ballast (alocar um `[]byte` gigante para inflar o alvo) era o truque
pré-Go 1.19; o limite de memória chegou no 1.19 justamente porque `GOGC` era o único botão [gc-guide]. Com 2 GB
compartilhados com bash, git, go test e o navegador, a configuração certa é a atual — `GOGC` alto + limite macio de
384 MB — e **não** `GOGC=off` com limite: o guia avisa para não fazer isso quando a memória é dividida com outros
programas desacoplados. Para `jazz -p` (processo de segundos) vale um degrau a mais: `SetGCPercent(-1)` até o fim do
parse de flags e do fast path, voltando para 400 se o processo for entrar no laço do agente; o limite de 384 MB
continua valendo. Medida: `gctrace` contado e `Maximum resident set size` do `/usr/bin/time -v`.

**3.3 Medir o que o GC custa de verdade numa sessão.** `GODEBUG=gctrace=1` em uma sessão interativa de 10 minutos
no Dell; somar o campo de CPU (`… ms cpu`) e dividir pelo tempo de CPU do processo (`/usr/bin/time -v` "User time").
Se passar de 5 %, atacar alocação (§4) antes de mexer em `GOGC`.

**3.4 Ponteiros custam GC; bytes não.** O alocador separa objetos sem ponteiros em spans `noscan` que o GC nunca
percorre (`src/runtime/malloc.go`). Um `[]niFile` com 7 `string` + 1 `[]string` por arquivo são 8–20 ponteiros por
arquivo para o GC visitar a cada ciclo; o mesmo índice como `[]byte` + `[]uint32` (§5.1) são 2 objetos `noscan`.
É a ideia "struct of arrays" do C traduzida para Go: menos ponteiros > menos bytes.

---

## 4. Alocação: onde o jazz aloca sem precisar

Ferramentas: `go build -gcflags='-m=1' 2>&1 | grep -E 'fuzzy.go|nameindex.go|intents.go'` mostra o que escapa
para o heap; `-benchmem` mostra allocs/op; `go test -memprofile` + `pprof -sample_index=alloc_space`.

**4.1 `fzDist` sem alocação (A4/A6).** Três mudanças, todas medidas no protótipo: (1) caminho ASCII que indexa
`string` direto (as chaves `fzKey` já são minúsculas sem acento; 99 % dos nomes de arquivo são ASCII) e só cai em
`[]rune` quando há byte ≥ 0x80; (2) as três linhas da matriz em `[65]int32` na pilha (chaves > 64 bytes caem no
caminho antigo); (3) `fzNeighbour` como tabela `[128][2]uint64` (2 KB, cabe no L1 de 32 KB) montada uma vez a partir
de `fzPos`. Resultado medido: 6103 → 408 ns, 3 → 0 allocs, igualdade testada. Esperado no Dell: o `resolve` de 5k
arquivos (A7) de ~0,5–1 s para 40–80 ms. Teste obrigatório: igualdade `fzDist` antigo × novo em todo o
`fuzzy_test.go` + fuzz (`go test -fuzz`) com pares aleatórios.

**4.2 `fzKey`/`fzFold` com caminho ASCII.** Hoje toda chamada passa por `norm.NFD.String` (aloca) + `strings.Builder`
(6 allocs, 196 ns no M4). Se `s` é ASCII (laço de bytes, `s[i] < 0x80`), minúsculas e remoção de separadores
cabem num único `make([]byte, 0, len(s))`; NFD só para não-ASCII. `niBuild` chama `fzKey` 3–5× por arquivo: é boa
parte das 30 alocações por arquivo do A8.

**4.3 `fzIntent` com índice, não varredura.** `fzIntent` percorre `fzVerbs` (~120 chaves) chamando `fzDist` em cada
uma (A9: 259 allocs). Com 4.1 vira 0 alloc; além disso, agrupar o vocabulário por comprimento (`[16][]string`) e só
comparar com comprimentos em `len±limite/fzEdit` corta ~70 % das comparações; ordem de iteração de mapa também é
aleatória, então trocar por slice ordenada deixa o desempate determinístico de graça. O mesmo vale para
`fzRepairLine`, chamado por `fpSet.Match` (A10).

**4.4 `[]byte` vs `string` e pré-alocação.** Conversões `string(b)` alocam (exceto em comparação, chave de mapa e
`range` que o compilador otimiza). Em `nameindex.symbols()`: `string(m[1])` para cada símbolo é necessária, mas o
`make([]byte, niSymBytes)` (48 KB) **por arquivo** não é — um buffer só reaproveitado no laço (o laço é sequencial).
Em `niBuild`, `ix.files` cresce por `append` desde zero: com `niMaxFiles` conhecido e uma contagem rápida do
`.git/index` (§5.2) dá para `make([]niFile, 0, n)`. `strings.Builder` com `Grow(n)` quando o tamanho é previsível.

**4.5 `sync.Pool`: quase nunca aqui.** O pool é esvaziado a cada GC e serve para objetos temporários reaproveitados
entre goroutines concorrentes [sync-pool]. Num núcleo só e num processo curto, um buffer em campo de struct (ou uma
variável de pacote protegida pelo mutex que já existe, como `niMu`) é mais simples e mais rápido. Exceção legítima:
buffers do parser SSE/streaming em sessão longa, se o perfil mostrar.

**4.6 Arenas: não.** `GOEXPERIMENT=arenas` está em espera indefinida e pode ser removido [arena-51317]. O equivalente
seguro é a "arena manual" do C: um `[]byte` grande com strings concatenadas e offsets `uint32` (§5.1).

---

## 5. Índice de nomes e hierarquia de memória

Drepper: o que custa é a falta de cache e de TLB, não a instrução; acesso sequencial é pré-buscado pelo hardware,
perseguir ponteiro não [drepper]. No Dothan: L2 de 2 MB, linha de 64 B, memória a ~100+ ns por falta. Um índice de
5k arquivos cabe inteiro no L2 **se for compacto**; o atual (2,5 MB vivos em 40k objetos, A8) não cabe e está
espalhado pelo heap.

**5.1 `niIndex` como struct of arrays.** Layout proposto (só um esboço da forma):

```go
type niIndex struct {
    blob    []byte    // todos os Rel concatenados; Base/Ext/Stem são fatias dele (zero cópia)
    relOff  []uint32  // início de cada Rel em blob (len = n+1)
    keyBlob []byte    // StemKey|CoreKey|DirKey concatenados, ASCII
    keyOff  []uint32  // 3 offsets por arquivo
    ext     []uint8   // índice numa tabela de extensões (<256 distintas)
    flags   []uint8   // bit0 Test, bit1 Doc
    keyLen  []uint8   // comprimento de StemKey: o filtro de comprimento lê 1 byte por arquivo
}
```

Mecanismo: o laço de `resolve` lê primeiro `keyLen` (5 KB contíguos, 80 linhas de cache) e só toca `keyBlob` dos
sobreviventes; nenhum ponteiro por arquivo → nada para o GC escanear (§3.4); 5k arquivos ≈ 150–250 KB no total.
`rels map[string]int` vira busca binária em `relOff` (os caminhos já saem ordenados do walk/`.git/index`), e
`bases map[string]bool` vira um mapa só de `hash32(base) → []uint32`, ou busca binária num vetor ordenado de bases.
Ganho esperado no Dell: `niBuild` quente de ~30 para ~5 alocações por arquivo → 10× menos alocação; `resolve`
mais 2–3× além do 4.1 por localidade. Medir com `BenchmarkNiBuild5k`/`NiResolve5k` + RSS.

**5.2 Fonte da lista de arquivos: `.git/index` em vez de `WalkDir` no HDD.** `filepath.WalkDir` já evita `stat` por
arquivo (usa o `d_type` do `getdents64`), mas cada diretório é um `open+getdents+close` e, a frio, uma busca de
cabeça no disco: ~350 diretórios × ~10 ms ≈ **3–4 s a frio** no HDD (estimativa; medir com o passo 5 do protocolo).
O `.git/index` é **um arquivo sequencial** com cabeçalho `DIRC`, versão, contagem e entradas ordenadas por caminho em
ordem de `memcmp`, cada uma com mtime/tamanho/modo e o caminho [git-index]. 5k entradas ≈ 400 KB: uma leitura
sequencial (≈ 10 ms a frio a 40 MB/s) e zero `getdents`. Parse em Go puro, ~80 linhas, sem `exec git` (que custaria
um fork+exec + carregar o git a frio). Arquivos não rastreados: um `WalkDir` só dos diretórios cujo mtime mudou
desde o índice salvo (§5.3), ou ignorar não rastreados no primeiro passo e completar sob demanda. Versões 3/4 do
índice (v4 comprime prefixos): suportar v2/v3 e cair no `WalkDir` caso contrário.

**5.3 Índice persistido, lido sem decodificar.** Salvar o 5.1 em `~/.cache/jazz-cli/ni/<hash-da-raiz>.idx`: cabeçalho
fixo (mágico, versão, n, mtime do `.git/index`, offsets das seções) + as seções como estão na memória (`uint32`
little-endian, que é a ordem nativa do 386). Na partida: `os.ReadFile` (ou `syscall.Mmap` com `PROT_READ`) e
`unsafe.Slice` sobre as seções — sem JSON, sem alocação por arquivo. mmap vs read: para **um** arquivo grande lido
uma vez por processo, mmap evita a cópia e deixa o kernel paginar só o que for tocado; para muitos arquivos pequenos
é pior — é a heurística do ripgrep, que só usa mmap quando sabe que vai buscar poucos arquivos [ripgrep]. Aqui é um
arquivo só: mmap ou `ReadFile`, medir os dois (diferença esperada pequena em 200 KB; o ganho grande é não decodificar
JSON). Invalidação: mtime+tamanho do `.git/index` e dos diretórios raiz. O TTL de 30 s de `niGet` vira "válido até o
mtime mudar".

**5.4 Aho-Corasick, FST e double-array trie para as entidades da spec (§2 do offline-brain).** O índice de ambiente
(binários do PATH, hosts SSH, serviços, contêineres, arquivos, símbolos) tem 10k–50k chaves. Opções:
- **Aho-Corasick em double-array** (Aoe, 1989): transição = `base[s]+c`, checada por `check[...]==s`; dois arrays de
  `int32`, sem ponteiros, busca O(n) no texto do pedido. É o que serve para "achar qualquer nome conhecido dentro da
  frase" (match exato, após `fzKey`).
- **FST** (transdutor de estados finitos, BurntSushi): conjunto ordenado comprimido que compartilha prefixos **e
  sufixos**, consultável direto do arquivo mapeado, e que admite busca aproximada intersectando com um autômato de
  Levenshtein [fst]. É o formato certo se o índice de símbolos passar de ~100k chaves; abaixo disso é complexidade
  demais para o ganho.
- Recomendação: para ≤ 50k chaves, vetor ordenado de chaves (`keyBlob`+`keyOff`) + busca binária para exato +
  varredura filtrada (§6) para aproximado. Medir antes de escrever um FST.

**5.5 `symbols()` lê até 3 000 arquivos a frio.** São 3 000 `open+read 48 KB` = no HDD, segundos. Ações: persistir
os símbolos no `.idx` (5.3), reextraindo só arquivos cujo mtime/tamanho mudou (o `.git/index` já tem os dois);
trocar as regexps `niSymRE` por um scanner de linha que testa o prefixo (`func `, `type `, `def `, `class `) com
`bytes.HasPrefix` antes de qualquer regexp — a maioria das linhas não começa assim.

---

## 6. Busca aproximada: algoritmos baratos de verdade

Hoje: `resolve` faz `fzDist` (DP O(m·n) ponderado) contra todo arquivo. Pipeline proposto, do mais barato ao mais
caro, cada estágio um **limite inferior** da distância ponderada (então não muda o resultado):

1. **Comprimento** (já existe): `|len(a)-len(b)| * fzEdit > lim` → descarta. Com `keyLen []uint8` é 1 byte lido.
2. **Bag de letras em 32 bits**: máscara `uint32` com um bit por letra a–z e um bit para dígitos, guardada por
   arquivo (`keyMask []uint32`). Uma substituição muda no máximo 2 bits, inserção/remoção no máximo 1; logo
   `edições ≥ ceil(OnesCount32(ma^mb)/2)`, e como a edição mais barata custa `fzSlip`, `dist ≥ fzSlip·edições`.
   Custo: um XOR e um popcount em tabela. Esperado: descarta a grande maioria dos 5k arquivos antes do DP (medir a
   taxa de descarte; é o número que decide se o resto vale a pena).
3. **Bit-paralelo de Myers/Hyyrö** em distância unitária: Myers (JACM 1999) computa Levenshtein em O(⌈m/w⌉·n) com
   palavras de w bits; Hyyrö (2003) estende para Damerau (transposição) [myers][hyyro]. Com chaves ≤ 32 letras,
   **uma palavra `uint32`** por coluna: ~15 operações de bit por caractere do texto, sem matriz. A distância
   unitária d₁ dá o limite `dist ≥ fzSlip·d₁` para a ponderada. Não substitui o `fzDist` ponderado (pesos
   2/4 e vizinhança de teclado não cabem no bit-vetor), mas é um filtro exato e muito mais barato que o DP.
   Em 386, usar `uint32` (2.2): `uint64` dobraria o custo.
4. **`fzDist` ponderado sem alocação** (4.1) só para os sobreviventes, com corte antecipado (já existe: `lo > max`).

Jaro-Winkler (citado na spec, ≥ 0,88): O(m·n) no pior caso pela janela de casamento, usa `float64` e não tem
limite inferior barato; serve como **segunda opinião** no desempate de 2–5 candidatos, nunca como varredura de
todos. Se adotado, calcular em inteiros (transposições e casamentos são contagens; só a fórmula final é razão).

Medida: contador de quantos candidatos passam em cada estágio (`dbg("ni", …)`) + `BenchmarkNiResolve5k`. Meta no
Dell: `resolve` de 1 token em 5k arquivos ≤ 20 ms quente.

---

## 7. NLU (spec §2.4): naive Bayes em inteiros e n-gramas por hash

Mecanismo: o NB multinomial soma `log P(c) + Σ log P(g|c)`. Fazer isso com `map[string]float64` por n-grama no
Dell = hash de string em software (2.4) + alocação de cada substring + `float64`. Proposta:
- **Hashing trick**: n-gramas de caracteres 3–5 do texto já normalizado, hash FNV-1a de 32 bits calculado
  incrementalmente sobre os bytes (sem criar substring), reduzido a `h & (2^k-1)` com k = 14–16 (16–64k baldes).
  Colisões custam pouca acurácia nesse volume (dataset de algumas centenas de frases); medir com k=12/14/16.
- **Log-probabilidades quantizadas**: tabela `[]int16` de `classes × baldes` com `round(log P · 256)` (ponto fixo
  8.8), gerada **em tempo de build** a partir de `nlu/intents.json` (`go generate`) e embutida como `[]byte` cru via
  `//go:embed`, lida com `unsafe.Slice` — zero decodificação na partida. 40 intenções × 16k baldes × 2 B = 1,3 MB;
  com k=14, 330 KB, cabe no L2. Inferência: ~100 n-gramas × 40 classes = 4k somas de `int32`: microssegundos.
- Probabilidade de saída (a spec exige "sempre com probabilidade"): softmax só no fim, sobre 40 somas, em `float64`.
- Teste de regressão: acurácia do NB em ponto fixo vs `float64` no dataset e no `blind_test.json` (diferença ≤ 0,5 pp).

---

## 8. Partida: do `execve` ao primeiro byte

**8.1 Regexps preguiçosas (A1/A2) — o maior ganho por esforço deste estudo.** 433 regexps compiladas no `init`, em
todo processo, quando um `jazz -p 'git status'` usa umas 10. Mecanismo: `regexp.MustCompile` constrói o programa
(`syntax.Parse` → `Simplify` → `Compile` → onepass), ~20 KB e ~130 alocações cada. Padrão já existente no repo para
copiar: `opRx()` em outparse.go (compila tudo num `sync.Once` na primeira chamada). Para variáveis soltas, Go 1.21+
tem `sync.OnceValue`: `var niCamel = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(…) })` e o uso
vira `niCamel().MatchString(x)`. Ordem de ataque: arquivos que **não** estão no caminho offline (`safety.go`,
`buildtarget.go`, `redact.go`, `docs*.go`, `guard_honest.go`, `hardening2.go`, `guardrails.go`, `quality.go`,
`route_task.go` – 280+ regexps). Ganho medido do teto: −8,98 MB e −55 939 alocações por processo; estimado no Dell:
−100 a −150 ms em todo comando e 0 GCs no `init`. Medida: `inittrace` (meta: `init main` < 0,5 MB). Trava contra
regressão: teste que roda `GODEBUG=inittrace=1` no próprio binário de teste e falha se `init main` passar de 1 MB.

**8.2 Várias regexps não precisam ser regexps.** `fpPunct` (`[\s?!.…;:]+$`), `niLineSx` (`:\d+(:\d+)?$`),
`niCamel`, `niSnake`, `niLetters`, `fpSafeArg` são testes de caractere que um laço de bytes resolve em nanossegundos
e sem o motor de regexp (o ripgrep foge do motor sempre que o padrão é literal [ripgrep]). Converter as que estão no
caminho quente de `niTokens`/`fpNormalize` (por token e por pedido).

**8.3 Dados embutidos: embutir já no formato de uso.** `intents.json`, `skills/triggers.json`, `diffrules.json`,
`guardrails_top.txt` são decodificados (JSON por reflexão) na primeira chamada; `fpBuild` ainda compila
`(?i)^(?:…)$` para cada padrão. Para o que for consultado por `jazz -p`, gerar em build (`go generate`) uma tabela Go
(literal de struct ou `[]byte` binário) e evitar `encoding/json` na partida; manter o JSON do usuário/projeto como
sobreposição decodificada só se o arquivo existir (`os.Stat` antes). Medida: `inittrace` + perfil de CPU do
`jazz --offline -p 'git status'`.

**8.4 Tamanho do binário e page-in do HDD.** O kernel mapeia o ELF e lê páginas sob demanda (falta de página maior +
read-ahead); a frio, só o que é tocado sai do disco. `/usr/bin/time -v` mostra "Major (requiring I/O) page faults":
é o número a acompanhar, não o tamanho do arquivo. `-s -w` (já no release) tira tabela de símbolos e DWARF, que não
são carregados na execução mas ocupam disco e cache de leitura. O `.bss` de 32 MiB de `crypto/internal/fips140/drbg`
que o `go tool nm` mostra é memória virtual zerada, não RSS — medir RSS, ignorar VSZ. Ações com efeito real no
page-in: 8.1 (o `init` toca código e dados de 433 regexps espalhados pelo binário) e não importar pacotes pesados
no caminho offline sem necessidade (o `net/http`/`crypto/tls` são inicializados mesmo offline; `inittrace` mostra
cada `init` e quanto aloca).

**8.5 UPX: não, em release assinado.** (1) **Integridade**: o `update.go` verifica sha256 assinado em ed25519 do
asset; um binário empacotado se autodescomprime para memória anônima e executa código que não é a imagem mapeada
do arquivo — scanners, `go version -m` e análise forense passam a ver o stub, não o programa. (2) **Memória**: o
mantenedor do UPX explica que o programa comprimido não é compartilhado entre processos simultâneos e não pode ser
paginado sob demanda a partir do arquivo [upx-fedora]; num Dell de 2 GB, o jazz + subagentes = N cópias privadas de
~13 MB que viram swap em vez de serem descartadas do page cache. (3) **CPU**: descomprimir 13 MB por partida no
Pentium M custa mais do que ler as poucas páginas tocadas do HDD. (4) Antivírus marcam empacotados com frequência.
O ganho de disco (13 → ~4 MB) não compensa nada disso.

---

## 9. PGO

Mecanismo: o compilador usa um perfil de CPU (`default.pgo` no diretório do pacote `main`, detectado por
`-pgo=auto`, padrão desde o Go 1.21) para inlinear chamadas quentes e devirtualizar interfaces; ganho típico de
2–14 % em CPU, pequeno aumento de binário [pgo]. O perfil é independente de arquitetura (um perfil do Mac serve para
o build 386), mas código específico de plataforma não se beneficia [pgo].

Ação: (1) `JAZZ_CPUPROFILE=path` em main.go (`pprof.StartCPUProfile` no início, `Stop` no `defer` e no caminho de
`os.Exit`); (2) coletar no Dell 10 perfis de duração parecida em uso real (`jazz --offline` com a suíte `jazz eval
offline`, 3 sessões interativas, `-p` em lote) — microbenchmarks são maus perfis [pgo]; (3) juntar com `go tool pprof
-proto a.pprof b.pprof … > default.pgo` e commitar; (4) refazer a cada release. Medida: `jazz eval offline` p50/p95
com e sem `-pgo=off`. **Fazer depois** de 8.1/4.1/5.x: PGO otimiza o código quente que existe; tirar alocação e
regexp do caminho muda o perfil inteiro.

---

## 10. Plan 9 assembly: quando vale (quase nunca)

Assembly em Go: arquivos `_386.s` com sintaxe Plan 9, `TEXT ·f(SB),NOSPLIT,$0-20`, sem inlining (toda chamada paga
o custo de chamada), mantido por arquitetura (9 alvos no release). Critério para escrever: (a) o perfil do **Dell**
mostra a função com > 10 % do tempo; (b) a versão em Go puro já foi otimizada (SWAR, tabela, sem alocação); (c) o
ganho medido > 2×. Candidato único hoje: busca de byte em 386, porque `bytealg.IndexByte` é `REPNE SCASB` (2.3).
Mas antes, em Go puro: SWAR de 4 bytes com o truque de Mycroft (`(w-0x01010101)&^w&0x80808080`) sobre `uint32`
lidos com `binary.LittleEndian.Uint32` — o compilador gera `MOVL` simples. Uma versão SSE2 (`PCMPEQB` + `PMOVMSKB`,
16 B por iteração, instruções que o Dothan tem) seria o passo seguinte só se o SWAR não bastar. Onde isso apareceria:
o scanner de linhas da sessão (§11), `symbols()`, `outparse` em saídas grandes. `math/bits` (`LeadingZeros32`,
`TrailingZeros32` viram `BSR/BSF` em 386) cobre o resto sem assembly. Medida: benchmark com 1 MB de texto real no Dell.

---

## 11. Kernel e syscalls numa máquina de um núcleo

**11.1 fork+exec.** O Go no Linux cria filhos com `clone(CLONE_VFORK|CLONE_VM)` (`src/syscall/exec_linux.go`): não
copia tabelas de página do processo pai, então o custo é o `execve` do alvo (carregar o ELF, o `ld.so`, a libc,
ler `/etc/ld.so.cache`) — com o bash, ainda o próprio bash. `posix_spawn`/`vfork` em C dariam o mesmo; não há o que
ganhar trocando o mecanismo. O que se ganha é **não criar processos**:
- O shell persistente (shell.go) já amortiza o bash: um `execve` por sessão em vez de um por comando. Manter.
- **Exec direto sem shell** quando o comando não tem metacaractere (`shellparse.go` já sabe tokenizar): `git status`,
  `go test ./...` vindos de `intents.go` podem ir por `exec.Command(argv...)` no one-shot (`partools.go`,
  `startDetached`), economizando um `execve` de bash por comando.
- One-shot que precisa de shell: `/bin/sh` (no antiX é o `dash`, bem menor que o bash) quando o dialeto não exige
  bash (`oneShotArgv` hoje usa sempre o bash detectado).
- Medida: `time (for i in $(seq 200); do bash -c true; done)` vs `dash -c true` vs `/bin/true` no Dell; multiplica
  pelo número de one-shots por tarefa do `jazz eval offline`.

**11.2 Sonda do bubblewrap por processo.** `sbProbeOS` roda `bwrap --ro-bind / / --dev /dev --unshare-pid
--die-with-parent -- /bin/true` a cada partida com sandbox: um fork+exec + criação de namespaces de usuário e pid +
montagens + exec do `/bin/true`. Ação: cachear o veredito em `~/.cache/jazz-cli/sandbox-probe` com chave
`(caminho, mtime e tamanho do bwrap, uname -r, conteúdo de /proc/sys/kernel/unprivileged_userns_clone se existir)`.
Cache errado no sentido "funciona" só faz o primeiro comando falhar alto (e aí invalida e sonda de novo); nunca
desliga o sandbox em silêncio. Medida: `strace -c -f` se existir, senão `/usr/bin/time -v` do `jazz --offline -p
'git status'` com e sem o cache.

**11.3 io_uring: existe no 5.10, não ajuda aqui.** Entrou no 5.1; o runtime do Go não usa (a proposta #31908 para
o `internal/poll` segue sem implementação; a fonte do Go 1.27 não tem nenhuma referência a io_uring) [go-31908]. Um
CLI que lê dezenas de arquivos pequenos e espera processos filhos não tem fila de I/O para amortizar; usar exigiria
syscalls cruas e o anel ainda é bloqueado por seccomp em vários sandboxes. Descartado.

**11.4 inotify vs polling.** Para invalidar o índice numa sessão interativa longa: inotify vigia **diretórios**
(não arquivos), ~350 vigias num repo de 5k arquivos, dentro do limite padrão de `max_user_watches` do 5.10 (8192;
o padrão passou a escalar com a RAM no 5.11) [inotify]. Custo: um goroutine bloqueado em `read` no fd, zero CPU
ocioso. Polling equivalente = `stat` de todos os diretórios a cada N s, que no HDD frio é o próprio problema. Mas:
para `jazz -p` (processo curto) nem um nem outro — a validação pelo mtime do `.git/index` (5.3) basta. inotify só no
modo interativo, e só se o perfil mostrar reconstrução do índice como custo.

**11.5 Page cache, e por que `find /` é péssimo.** O kernel guarda dentries/inodes e páginas de arquivo na RAM livre;
com 2 GB isso é pouco. `find /` (o vício do César, FINDINGS) lê os metadados do disco inteiro: centenas de milhares
de buscas de cabeça no HDD (minutos) e, pior, **expulsa do cache** os inodes do repo e as páginas do próprio jazz —
o próximo `niBuild` e a próxima partida voltam a ser frios. Ações: o guarda de comandos já existe; adicionar uma
regra de custo: `find /`, `find ~`, `grep -r /`, `du /` sem `-xdev`/caminho restrito → bloquear com explicação e
oferecer o índice de ambiente (spec §2) ou `locate` se `mlocate` estiver instalado. Medida: `free -m` (coluna
buff/cache) e `niBuild` frio antes/depois de um `find /` no Dell.

**11.6 O que o ripgrep ensina e o que não se aplica.** Velocidade do rg: literal extraído da regexp e buscado com
`memchr` SIMD de 16 B por iteração, escolhendo o byte **mais raro** do literal por tabela de frequência de 256
bytes; motor de regexp só quando necessário; `.gitignore` com semântica completa para não descer em
`node_modules`/`target`; mmap só para poucos arquivos [ripgrep]. Paralelismo de diretórios: **não se aplica** num
núcleo. Para o jazz: (a) os fast paths de busca já preferem `rg` quando existe (`matchNorm`); garantir que o
`envprobe` registre `rg` e que o `install_tool` ofereça `ripgrep` no Dell (pacote Debian, binário estático 386);
(b) o `grep` de fallback já exclui `.git`/`node_modules`/`vendor`; adicionar `--exclude-dir` para `target`, `dist`,
`build`, `.venv` igual ao `rtSkipDir`; (c) a ideia do "byte raro" serve para o nosso SWAR (§10): procurar o byte
menos frequente da agulha, não o primeiro.

---

## 12. Sessões JSONL

**12.1 Listagem sem parse completo (A12).** `jazz -c` só precisa da sessão mais nova: `os.ReadDir` + `Info().ModTime()`
e ordenar — zero leitura de conteúdo. Para `/sessions` e `--resume`, que mostram título/modelo/contagem: arquivo
lateral `<id>.meta` (uma linha JSON pequena, reescrita no `Close`/`Sync` da sessão) e cache por (tamanho, mtime);
só sessões sem `.meta` válido caem no parse completo. Ganho estimado no Dell com 300 sessões de ~200 KB: de 60 MB lidos
e decodificados por reflexão (dezenas de segundos a frio) para 300 `stat` + 300 leituras de 200 B. Medida: passo (f)
do protocolo.

**12.2 Quando o parse completo for necessário:** pré-filtro com `bytes.Contains(line, []byte(`"t":"msg"`))` antes do
`json.Unmarshal` (a maioria das linhas de evento é descartada sem reflexão); um buffer de 1 MB só para o laço inteiro,
não por arquivo. Atenção à verificação HMAC de `hardening2.go`: o `.meta` é cache, nunca fonte de verdade para
retomar — ao retomar, o arquivo principal é lido e verificado como hoje.

**12.3 Escrita.** `writeLocked` já faz um `Write` por evento sem `fsync` (o `Sync` é só em fim anormal — certo para o
HDD: `fsync` custa uma volta de disco, 5–15 ms). Manter. O `append(b, '\n')` pode realocar; o `h2SignLine` já cria o
buffer com folga — deixar o `'\n'` dentro da mesma capacidade (`len(b)+41`) evita uma alocação por evento.

---

## 13. Ideias de C traduzidas para Go idiomático

| C | Go no jazz | Exemplo concreto |
|---|---|---|
| struct of arrays | slices paralelos de tipos sem ponteiro | `niIndex` (§5.1), tabela do NB (§7) |
| arena / pool de strings | um `[]byte` + offsets `uint32`; `unsafe.String(&blob[o], n)` só onde a vida útil é a do índice | `blob`/`keyBlob` |
| buffer de capacidade fixa na pilha | `var row [65]int32` com fallback para slice | `fzDist` (§4.1) |
| tabela de consulta por byte | `[256]uint8` / bitset `[128][2]uint64` | `fzNeighbour`, classe de caractere em `niTokens` |
| `mmap` de arquivo binário | `syscall.Mmap` + `unsafe.Slice`, ou `os.ReadFile` + `unsafe.Slice` | `.idx` (§5.3) |
| `const` gerado em build | `go generate` + `//go:embed` de `[]byte` cru | log-probs do NB, intents compilados |
| `inline` + sem alocação | funções pequenas, sem interface no laço; conferir com `-gcflags=-m` | `zzNeighbour` do protótipo inlinea |

Regra de ouro do `unsafe` aqui: só leitura, sobre memória que vive tanto quanto o índice, ordem de bytes conferida no
cabeçalho do arquivo (o release também sai para arm/arm64, todos little-endian, mas o teste tem que existir).

---

## 14. O que NÃO fazer

- **Assembly prematuro**: nada de `.s` antes de SWAR em Go puro e de um perfil do Dell provando > 10 % (§10).
- **cgo**: quebra o binário estático (`CGO_ENABLED=0` do release), exige toolchain C cruzado para 9 alvos, cada
  chamada custa dezenas de ns de troca de pilha e o GC não enxerga a memória do C.
- **UPX** em release assinado (§8.5).
- **Spam de goroutines num núcleo**: `GOMAXPROCS=1` efetivo; goroutines para "paralelizar" `niBuild`, `symbols()` ou
  o walk só adicionam troca de contexto e pilhas de 2 KB. O I/O de disco no HDD é serial de qualquer forma (uma
  cabeça). Goroutines são para **esperar** (shell, rede, timer), não para calcular. `--subagents-parallel` e
  `--parallel-tools` devem defaultar para 1 quando `runtime.NumCPU()==1` (conferir em `subagent.go`/`partools.go`).
- **`GOGC=off` sem limite**, ballast, ou `GOMEMLIMIT` acima de ~25 % da RAM no Dell.
- **`float64` e mapas em laço quente** (2.2, 2.4).
- **io_uring**, `GOEXPERIMENT=arenas`, FST antes de medir que o vetor ordenado não basta.
- **Otimizar sem a linha de base da §1** registrada em `results/`.

---

## 15. Top 10 por ganho/esforço

| # | Mudança | Onde | Esforço | Ganho esperado (Dell) | Como medir |
|---|---|---|---|---|---|
| 1 | Regexps preguiçosas (`sync.OnceValue`/`opRx`-style), começando pelos arquivos fora do caminho offline | `safety.go`, `buildtarget.go`, `redact.go`, `docs*.go`, `guard_honest.go`, … | P (mecânico, 1–2 dias) | −9 MB e −56k allocs por processo; −100 a −150 ms em **todo** comando | `GODEBUG=inittrace=1` (meta < 0,5 MB); `/usr/bin/time` de `jazz version` |
| 2 | Ajuste de GC antes do `init` do `main` (pacote `internal/early`) + `SetGCPercent(-1)` no `-p` até o fast path | novo `internal/early`, `ui_width.go` | P (horas) | 4 GCs → 0 na partida; menos GC no `-p` | `GODEBUG=gctrace=1 … | grep -c '^gc '` |
| 3 | `fzDist` só-ASCII, sem alocação, tabela de vizinhos em bitset | `fuzzy.go` | P (protótipo pronto: 15×, 0 allocs) | `resolve` 5k: ~0,5–1 s → 40–80 ms | `BenchmarkFzDist`, `BenchmarkNiResolve5k` no Dell |
| 4 | `jazz -c` por mtime; `.meta` lateral para listagem | `session.go` (`sessListDir`, `pfFindSession`) | P–M | `-c` com 300 sessões: segundos → milissegundos | passo (f) do protocolo, a frio |
| 5 | Filtros de limite inferior antes do DP (comprimento em `[]uint8`, bag de letras `uint32`) | `nameindex.go` (`resolve`, `uniqueWhy`, `niSimilarSymbols`) | P | mais 3–10× sobre o #3 (depende da taxa de descarte) | contador por estágio + benchmark |
| 6 | Cache do veredito da sonda do bwrap | `sandbox.go` (`sbProbeOS`) | P | −1 fork+exec+namespaces por partida (dezenas de ms) | `/usr/bin/time -v jazz --offline -p 'git status'` |
| 7 | Lista de arquivos do `.git/index` (parse Go) em vez de `WalkDir` | `nameindex.go` (`niBuild`) | M | `niBuild` frio em 5k: segundos → dezenas de ms | passo 5 do protocolo com `drop_caches` |
| 8 | `niIndex` em struct of arrays + persistência `.idx` sem decodificação (inclui símbolos) | `nameindex.go` | M–G | 10× menos alocação; partida do índice ~0 quente; RSS menor | `BenchmarkNiBuild5k`, RSS, `inittrace` |
| 9 | `fzKey`/`fzFold`/`fzIntent`/`fzRepairLine` sem NFD para ASCII e com vocabulário por comprimento; testes de caractere sem regexp em `niTokens`/`fpNormalize` | `fuzzy.go`, `intents.go`, `nameindex.go` | P | `fpSet.Match` com typo: 327 → < 30 allocs; `niBuild` −50 % alloc | `BenchmarkFzIntent`, `BenchmarkFpMatchTypo` |
| 10 | PGO com perfis reais do Dell (`JAZZ_CPUPROFILE` + `default.pgo`), **depois** de 1–9 | `main.go`, raiz do repo | P (+ disciplina por release) | 2–14 % de CPU no caminho quente | `jazz eval offline` p50/p95 com e sem `-pgo=off` |

Fora do top 10, mas na fila: exec direto sem shell para comandos sem metacaractere (11.1); regra de custo contra
`find /` (11.5); NB em ponto fixo embutido (§7, entra junto com o `nlu.go` da spec); SWAR para busca de byte (§10)
só se o perfil pedir.

---

## Resultados (Mac)

Branch `perf-09`, itens 1–9 do top 10 (o 10, PGO, ficou de fora; `JAZZ_CPUPROFILE`/`JAZZ_TRACE` já existem para
coletar os perfis no Dell). Apple M4, darwin/arm64, Go 1.27.2. **Números do Dell ainda não medidos**: rodar o protocolo
da §1 com o binário 386 desta branch.

| # | Medida | Antes | Depois |
|---|---|---|---|
| 1 | `init main` (`GODEBUG=inittrace=1 jazz version`) | 9.540.504 B, 59.443 allocs | 246.024 B, 1.587 allocs |
| 1–2 | GCs antes de imprimir `version` / no `-p 'abre o README.md'` | 4 / 8 | 0 / 0 |
| 1–2 | `jazz version` p50 (parede) | 29,7 ms (40 ms em outra rodada) | 4,6 ms |
| 3 | `BenchmarkFzDist16` | 6.530 ns, 3 allocs | 506 ns, 0 allocs |
| 3+9 | `BenchmarkFzIntent` | 53,6 µs, 259 allocs | 4,2 µs, 0 allocs |
| 3+9 | `BenchmarkFpMatchTypo` | 48,0 µs, 327 allocs | 7,4 µs, 16 allocs |
| 3 | `BenchmarkNiResolve5k` | 36,1 ms, 15.915 allocs | 2,5 ms, 165 allocs (o resto é `os.Stat` do recency) |
| 5 | `BenchmarkNiResolvePlain5k` (typo sem candidato) | 163,8 µs | 21,5 µs |
| 4 | `jazz -c` com 300 sessões (`BenchmarkSessContinue300`) | 48,4 ms, 352 MB alocados | 0,56 ms, 219 KB |
| 4 | listar 300 sessões com `.meta` quente | 48,4 ms | 3,6 ms |
| 6 | sonda do sandbox (`BenchmarkSbProbe`) | 5,42 ms (sandbox-exec) | 65 µs do cache |
| 7 | lista de 5k arquivos (`BenchmarkNiList5k`) | WalkDir 8,25 ms | `.git/index` 1,87 ms |
| 9 | `BenchmarkFzKey` | 223 ns, 6 allocs | 35 ns, 1 alloc |
| 8+9 | `niBuild` 5k frio (`BenchmarkNiBuild5k`) | 26,6 ms, 148.862 allocs | 10,1 ms, 33.421 allocs |
| 8 | `niBuild` 5k com `.idx` válido (`BenchmarkNiBuildIdx5k`) | — | 0,71 ms, 1.216 allocs (load: 14 allocs) |
| extra | execs de `git` em `jazz --offline -p 'abre o README.md'` | 11 (6× `rev-parse --show-toplevel`) | 5 |
| todos | `jazz --offline -p 'abre o README.md'` p50 | 132,7 ms | 72,2 ms |
| todos | `jazz --offline -p 'git status'` p50 | 170,5 ms | 115,1 ms |

O que sobra no `-p` (trace com `JAZZ_TRACE`): quase todo o tempo de parede é espera de processos filhos — `git branch`,
`git status --porcelain`, `git log` do `envBlock` (o prompt de sistema é montado em `pfStartSession` **antes** do fast
path, mesmo quando a resposta sai sem modelo), `git check-ignore` da cerca e o `rev-parse` restante. No Dell cada exec
de git custa dezenas de ms; montar o prompt só quando o modelo for chamado é o próximo ganho grande (fora deste top 10:
muda a ordem de gravação da sessão, precisa de decisão).

Diferença de comportamento conhecida (item 7): na primeira construção do índice de um repo (sem `.idx`), um arquivo
não rastreado criado num subdiretório antes de um arquivo rastreado do mesmo diretório ser alterado no lugar e o
`.git/index` ser regravado não aparece; o diretório fica marcado como não verificado e a construção seguinte o lê
(`TestGitIndexKnownGap`, `TestNiIdxClosesGitGap`). A raiz é sempre lida.

---

## Fontes

- [gc-guide] A Guide to the Go Garbage Collector — https://go.dev/doc/gc-guide
- [pgo] Profile-guided optimization — https://go.dev/doc/pgo
- [diagnostics] Go Diagnostics (`inittrace`, `gctrace`, `runtime/trace`, uma ferramenta por vez) — https://go.dev/doc/diagnostics
- [go-env] `go help environment` / `cmd/go` docs (GO386: `sse2` padrão, `softfloat`) — https://pkg.go.dev/cmd/go#hdr-Environment_variables
- Fonte do Go 1.27.2 lida localmente: `src/internal/bytealg/indexbyte_386.s` (REPNE SCASB), `equal_386.s`/`compare_386.s`
  (SSE2), `bytealg.go` (`MaxLen`), `src/internal/runtime/maps/runtime_alg.go` (hash AES exige AES+SSSE3+SSE4.1),
  `src/runtime/stack.go` (`stackMin = 2048`), `src/runtime/malloc.go` (spans noscan), `src/syscall/exec_linux.go`
  (`CLONE_VFORK|CLONE_VM`) — https://go.dev/src/
- [sync-pool] `sync.Pool` e `sync.OnceValue` — https://pkg.go.dev/sync
- [arena-51317] proposal: arena (em espera) — https://go.dev/issue/51317 ; memory regions — https://go.googlesource.com/proposal/+/HEAD/design/70257-memory-regions.md
- [go-31908] internal/poll: suporte a io_uring — https://go.dev/issue/31908
- [agner-asm] Agner Fog, Optimizing subroutines in assembly language, §13 (busca de byte com SWAR) e §16.9 (string instructions) — https://www.agner.org/optimize/optimizing_assembly.pdf ; tabelas de instruções — https://www.agner.org/optimize/
- [drepper] Ulrich Drepper, What Every Programmer Should Know About Memory (2007) — https://people.freebsd.org/~lstewart/articles/cpumemory.pdf
- [ripgrep] Andrew Gallant, ripgrep is faster than {grep, ag, git grep, ucg, pt, sift} — https://burntsushi.net/ripgrep/
- [fst] Andrew Gallant, Index 1,600,000,000 Keys with Automata and Rust — https://burntsushi.net/transducers/
- [myers] G. Myers, A fast bit-vector algorithm for approximate string matching based on dynamic programming, JACM 46(3), 1999 — https://doi.org/10.1145/316542.316550
- [hyyro] H. Hyyrö, A bit-vector algorithm for computing Levenshtein and Damerau edit distances, Nordic J. Computing 10(1), 2003 — https://www.Gwern.net/doc/cs/algorithm/2003-hyyro.pdf ; Hyyrö, Explaining and extending the bit-parallel algorithm of Myers (2001) — https://trepo.tuni.fi/bitstream/10024/67325/1/951-44-5840-0.pdf
- J. Aoe, An efficient digital search algorithm by using a double-array structure, IEEE TSE 15(9), 1989 — https://doi.org/10.1109/32.31365
- [git-index] Git index format — https://git-scm.com/docs/index-format
- [inotify] inotify(7) (limites em `/proc/sys/fs/inotify/`) — https://man7.org/linux/man-pages/man7/inotify.7.html
- Linux `vm.drop_caches` — https://docs.kernel.org/admin-guide/sysctl/vm.html
- [upx-fedora] mantenedor do UPX sobre compartilhamento e paginação sob demanda — https://lists.fedoraproject.org/pipermail/test/2013-May/115642.html ; UPX — https://upx.github.io/
- Brendan Gregg, The USE Method (utilização/saturação/erros por recurso: CPU única e disco são os dois recursos saturados do Dell) — https://www.brendangregg.com/usemethod.html
- [intel-735] Intel Pentium M 735 (2M cache, 1.70 GHz, 400 MHz FSB) — https://intel.com/content/www/us/en/products/sku/27588/intel-pentium-m-processor-735-2m-cache-1-70a-ghz-400-mhz-fsb/ordering.html
