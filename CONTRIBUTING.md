# Contribuindo

Obrigado pelo interesse. *English is welcome too: issues and PRs in English or Portuguese are fine.*

## Antes de abrir um PR

- Bug: abra uma issue com `jazz version`, o sistema (`uname -a` ou a versão do Windows) e, se puder,
  o pacote do `jazz bug` (ele mascara chaves, mas confira antes de anexar).
- Vulnerabilidade: **não** abra issue pública; siga o [`SECURITY.md`](SECURITY.md).
- Mudança grande (ferramenta nova, novo provedor, mudança no prompt): abra uma issue antes para
  combinar o desenho. O [`DESIGN.md`](DESIGN.md) lista o que o projeto decidiu e o que rejeitou.

## Ambiente

Go 1.27 ou mais novo. Sem dependências além de `golang.org/x/{sys,text}`; um PR que adiciona
dependência precisa de um motivo forte.

```sh
gofmt -l .                       # não pode imprimir nada
go vet ./...
go test -count=1 ./...
CGO_ENABLED=0 GOOS=linux GOARCH=386 go build -o /dev/null .      # o alvo de 32 bits continua compilando
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o /dev/null .
sh -n scripts/install.sh
```

O CI roda tudo isso, mais os testes no `windows-latest`, `govulncheck` e `gitleaks`.

## Convenções

- Texto que o usuário vê: português do Brasil. Prompts para o modelo e comentários de código: inglês.
- Mudança pequena e com teste. Comportamento novo vem com chave para desligar (flag, variável
  `JAZZ_*` e campo do `config.json`), para dar para comparar A/B nas evals.
- Nada de segredo real em teste: use valores obviamente falsos (`sk-ant-TESTKEY-0000…`).
- Mudou algo que afeta a segurança (guardas, sandbox, confiança da pasta)? Rode a suíte
  `evals/redteam` (ver README) e diga no PR o que mediu. Não escreva número que você não mediu.
- Mensagens de commit curtas, no imperativo, em português ou inglês.

## Licença

Ao contribuir você concorda que sua contribuição é licenciada sob a [licença MIT](LICENSE) do projeto.
Todos seguem o [Código de Conduta](CODE_OF_CONDUCT.md).
