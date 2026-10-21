# Publicando o jazz-cli no GitHub

Roteiro para o dono do projeto. Nada aqui foi executado: o repositório remoto ainda não existe.
Rode os comandos só depois de decidir publicar. A versão de cada tag precisa ser igual a
`var version` em `main.go` (hoje `0.7.0`, tag `v0.7.0`).

## 0. Antes de tudo

```sh
cd ~/jazz-cli
git status -sb                      # limpo
git log --oneline public | head -3  # a branch `public` tem UM commit, histórico limpo
gh auth status                      # logado com permissão na org jazzautomations
```

A branch `public` é a árvore de `main` num commit único, sem o histórico privado. Se `main` mudar
depois, refaça a `public` (seção 7) antes de empurrar.

## 1. Chave de assinatura (uma vez, guarde fora do repositório)

```sh
go run ./scripts/sign -gen ~/.config/jazz-release.key   # imprime a chave pública
go run ./scripts/sign -pub ~/.config/jazz-release.key   # imprime de novo (base64)
```

Faça backup da chave privada (gerenciador de senhas). Quem a perder não consegue mais publicar
atualizações aceitas por binários já instalados. O arquivo `*.key` já está no `.gitignore`.

## 2. Criar o repositório e empurrar a `public` como `main`

```sh
gh repo create jazzautomations/jazz-cli --public \
  --description "jazz-cli: agente de código no terminal, em Go" --disable-wiki
git remote add origin https://github.com/jazzautomations/jazz-cli.git
git push origin public:main
git remote -v
```

Não empurre `main` local nem as outras branches. O CI (`.github/workflows/ci.yml`) roda no
primeiro push; veja a seção 6 se algo falhar.

## 3. Segredo e variável da assinatura

```sh
gh secret set JAZZ_SIGN_KEY -R jazzautomations/jazz-cli < ~/.config/jazz-release.key
gh variable set JAZZ_UPDATE_PUBKEY -R jazzautomations/jazz-cli \
  --body "$(go run ./scripts/sign -pub ~/.config/jazz-release.key)"
```

`JAZZ_UPDATE_PUBKEY` faz o build falhar se o segredo for a chave errada. Sem `JAZZ_SIGN_KEY` o
release sai SEM assinatura, marcado como pre-release, e os instaladores só conferem o sha256.

O job `secrets` do CI usa `gitleaks/gitleaks-action@v2`. Em repositório de organização esse action
exige a licença `GITLEAKS_LICENSE` (gratuita para uso aberto, pedida em gitleaks.io). Sem ela o job
falha; ou registre o segredo, ou troque o job por `gitleaks detect` num passo `run`.

## 4. Release: tag que dispara `release.yml`

Espere o CI do `main` ficar verde e então:

```sh
git checkout public
git tag -a v0.7.0 -m "jazz-cli v0.7.0"
git push origin v0.7.0
gh run watch -R jazzautomations/jazz-cli      # acompanha o workflow `release`
```

O workflow roda `go vet` e `go test`, builda os 7 binários com `scripts/release.sh` e publica em
`dist/release/*`. Assets esperados (o job confere um por um):

```
jazz-linux-386  jazz-linux-amd64  jazz-linux-arm64
jazz-darwin-amd64  jazz-darwin-arm64
jazz-windows-amd64.exe  jazz-windows-arm64.exe
checksums.txt  manifest.txt  VERSION  install.sh  install.ps1
manifest.txt.sig  manifest.txt.minisig  checksums.txt.sig  checksums.txt.minisig   (só assinado)
```

Se a tag foi criada errada: `git push origin :refs/tags/v0.7.0 && git tag -d v0.7.0`, apague a
release no GitHub (`gh release delete v0.7.0 -R jazzautomations/jazz-cli --yes`) e refaça.

## 5. Verificar a release e os instaladores

```sh
gh release view v0.7.0 -R jazzautomations/jazz-cli --json assets,isPrerelease \
  --jq '{pre: .isPrerelease, assets: [.assets[].name]}'     # pre=false se assinou
```

Linux e macOS (em máquina limpa ou com `JAZZ_BIN_DIR=$(mktemp -d)`):

```sh
curl -fsSL https://github.com/jazzautomations/jazz-cli/releases/latest/download/install.sh | sh
jazz version        # jazz-cli 0.7.0
jazz doctor
jazz update --check # diz que já está na última
```

O instalador oficial carrega a chave pública; a saída não deve ter o aviso "assinatura NÃO
verificada" (no macOS o `openssl` é LibreSSL: instale `brew install openssl@3` ou `minisign`, ou
use `JAZZ_OPENSSL=/opt/homebrew/opt/openssl@3/bin/openssl`). Teste também o caminho de erro: `sh
install.sh --require-sig` tem de falhar se a verificação não for possível.

Windows (PowerShell):

```powershell
irm https://github.com/jazzautomations/jazz-cli/releases/latest/download/install.ps1 | iex
jazz version
```

Linux 32 bits (i386/i686): o mesmo comando do Linux baixa `jazz-linux-386`.

## 6. Se o CI falhar no windows-latest

O job `windows` nunca rodou de verdade (só `go vet` com GOOS=windows e builds cruzados foram
verificados). Esperado que algo apareça. Em ordem:

1. Abra o log: `gh run view --log-failed -R jazzautomations/jazz-cli`.
2. Falha em `go vet` ou `go build`: quase sempre um arquivo sem a build tag certa
   (`*_unix.go` / `*_windows.go`). Reproduza com `GOOS=windows go vet ./...` no Mac.
3. Falha em `go test`: teste que assume POSIX (caminhos com `/`, `sh`, `bash`, `sandbox-exec`,
   permissões 0600, fim de linha). Corrija com `t.Skip` condicionado a `runtime.GOOS == "windows"`
   e uma nota no teste dizendo por quê; os goldens de `testdata` são LF e o job já desliga o
   autocrlf.
4. Falha só no `jazz.exe version`: confira se `-ldflags` não tirou a versão.
5. Enquanto não corrigir, não bloqueie o release do Linux/macOS: o `release.yml` não depende do job
   `windows`. Mas os binários `.exe` ficam sem teste; avise isso nas notas da release.
6. Corrija no `main` local, refaça a `public` (seção 7) e empurre de novo com `--force-with-lease`
   enquanto ninguém mais usa o repositório; depois de o repositório ter forks, passe a empurrar
   commits normais.

Outras falhas conhecidas possíveis: `govulncheck` achando vulnerabilidade na biblioteca padrão
(suba a versão do Go em `setup-go`), `shellcheck` com aviso novo em `scripts/install.sh`.

## 7. Refazer a branch `public`

```sh
git checkout main
git branch -D public
git checkout --orphan public
git add -A
git commit -m "jazz-cli v0.7.0: agente de código no terminal (histórico público inicial)"
git checkout main
```

Antes de empurrar, rode a varredura de dados pessoais (tem de dar zero resultados):

```sh
git grep -nIE '/Users/[a-z]|/home/[a-z]+/|100\.[0-9]+\.[0-9]+\.[0-9]+|[a-z0-9._-]+@(gmail|hotmail|outlook)\.' public
git grep -nIiE 'scratchpad|\.claude/worktrees|password *[:=]' public
```

Acrescente à mão o nome da sua máquina, seu usuário e qualquer IP ou senha que você lembre; este
arquivo não lista esses valores de propósito. Hits esperados: `blockux_test.go` (`/Users/x`, dado
fictício) e `improve.go` (uma regex que cita `scratchpad`); os dois são código, não dado pessoal.

## 8. Depois de publicar

- Em Settings > Branches, proteja `main` (exigir CI verde).
- Ative "Private vulnerability reporting" (o `SECURITY.md` manda reportar por lá).
- Nunca empurre `main` local: ele guarda o histórico privado.
