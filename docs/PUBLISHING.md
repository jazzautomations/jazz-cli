# Publicando o jazz-cli no GitHub

Este roteiro descreve uma publicação normal no repositório público `jazzautomations/jazz-cli`.
Faça o release a partir de um commit revisado da branch pública `main`; não reescreva o histórico
público para sincronizá-lo com uma cópia de desenvolvimento privada.

## Antes do release

Confirme que a árvore pública está limpa, os testes e o CI passaram, e a versão em `main.go`
corresponde à tag `vX.Y.Z`. Revise o diff e os arquivos incluídos no commit. Não publique notas de
trabalho, resultados privados ou `HANDOFF.md`.

A release é disparada por uma tag `v*` (ver `.github/workflows/release.yml`). A workflow verifica
`go vet ./...`, `go test -count=1 ./...` e a sintaxe de `scripts/install.sh`, então compila os oito
binários e os arquivos de manifesto/checksum usados pelos instaladores. Antes de criar a tag,
confirme a configuração do segredo opcional `JAZZ_SIGN_KEY` e da variável `JAZZ_UPDATE_PUBKEY`.
Sem a chave, a workflow publica um pré-lançamento não assinado; os instaladores verificam o SHA-256
e avisam que não há assinatura. Só trate o release como assinado quando a workflow indicar isso.

## Commit e release

Faça commits normais na branch pública `main`, revise o conteúdo do commit e envie pelo fluxo usual
do repositório (pull request ou push autorizado). Não empurre uma branch de desenvolvimento privada
para substituir `main`. Depois que o CI passar:

```sh
git checkout main
git pull --ff-only origin main
git status --short
git show --stat --oneline HEAD
# Confira que a versão de main.go é X.Y.Z antes de continuar.
git tag -a vX.Y.Z -m "jazz-cli vX.Y.Z"
git push origin vX.Y.Z
gh run watch -R jazzautomations/jazz-cli
```

Se uma tag ou release estiver incorreta, pare e decida com os mantenedores como corrigir. Não apague
tags/releases nem force-push histórico público como rotina. Prefira um novo commit corretivo e, quando
apropriado, uma versão corrigida.

## Conferir o resultado

Confira a execução da workflow e a lista de assets na página do release. Espera-se:


```
jazz-linux-386  jazz-linux-amd64  jazz-linux-arm64  jazz-android-arm64
jazz-darwin-amd64  jazz-darwin-arm64
jazz-windows-amd64.exe  jazz-windows-arm64.exe
checksums.txt  manifest.txt  VERSION  install.sh  install.ps1
manifest.txt.sig  manifest.txt.minisig  checksums.txt.sig  checksums.txt.minisig (somente assinado)
```

Uma release assinada contém 18 assets: nove binários, cinco arquivos base e quatro assinaturas.
O instalador do Termux seleciona Android/arm64. Cross-build não substitui teste em aparelho;
a validação em dispositivo real ainda está pendente.

Em uma máquina de teste, instale usando o comando exibido no site, confira `jazz version` e rode
`jazz doctor`. Confirme também que a assinatura é validada quando a release é assinada; em macOS,
LibreSSL pode exigir OpenSSL 3 ou `minisign` conforme as instruções do instalador. Teste o instalador
PowerShell em Windows e a arquitetura i386 em Linux quando disponíveis.


## Depois

Mantenha a proteção de branch e CI exigido, revisão de commits e configuração de reporte de
vulnerabilidades do GitHub. Para atualizar o site, siga o workflow de deploy configurado no
repositório e confirme o resultado publicado; este documento não autoriza publicar a partir de uma
árvore privada ou incompleta.
