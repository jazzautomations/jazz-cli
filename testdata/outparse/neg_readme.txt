# jazz-cli

Agente de código no terminal, escrito em Go (v0.3). Um binário estático só (~9 MB), sem
dependências: roda num Mac M-series e também num Pentium M de 32 bits com 2 GB de RAM
(o `oldbox`, linux/386).

Ele lê arquivos, roda comandos num shell persistente, edita código e cria arquivos,
falando com o modelo que você escolher: Anthropic, OpenAI, ChatGPT (login com a conta),
OpenRouter, Groq, Gemini, Mistral, DeepSeek, Ollama/llama.cpp/LM Studio e mais uma
dúzia de provedores grátis. O roteador **omni** distribui as requisições entre todos os
provedores que você tem, com os grátis na frente.

Princípios (detalhes em [`DESIGN.md`](DESIGN.md)):

- Poucas ferramentas (`read`, `write`, `edit`, `bash`, `todo`, `fetch`, `task`) e um prompt
  pequeno, que funciona até com modelo grátis e contexto curto.
- Prompt de sistema e schemas fixos durante a sessão, pra aproveitar o cache de prompt.
- Interface leve (editor de linha + ANSI), sem flicker por SSH.
- Comando perigoso (rm -rf amplo, push --force, curl | sh, sudo…) sempre pede confirmação.

## Instalação

### Mac (Apple Silicon)

```sh
cd ~/jazz-cli
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 /opt/homebrew/bin/go build -trimpath -ldflags="-s -w" -o dist/jazz-darwin-arm64 .
mkdir -p ~/.local/bin && install -m 755 dist/jazz-darwin-arm64 ~/.local/bin/jazz
command -v jazz || echo 'adicione ao ~/.zshrc:  export PATH="$HOME/.local/bin:$PATH"'
```

### oldbox (linux/386, sem sudo)

Do Mac, com a chave SSH já configurada (`ssh oldbox`):

```sh
scripts/deploy-oldbox.sh          # compila linux/386, instala em ~/.local/bin/jazz, ajusta o PATH
scripts/deploy-oldbox.sh --sudo   # instala em /usr/local/bin (o sudo pede a senha no próprio oldbox)
```

O script também copia `scripts/jazz-cli.desktop` para `~/.local/share/applications/`
(atalho no menu: abre um terminal com o jazz).

Na mão:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=386 /opt/homebrew/bin/go build -trimpath -ldflags="-s -w" -o dist/jazz-linux-386 .
ssh oldbox 'mkdir -p ~/.local/bin ~/.local/share/applications'
scp dist/jazz-linux-386 oldbox:.local/bin/jazz && ssh oldbox 'chmod 755 ~/.local/bin/jazz'
scp scripts/jazz-cli.desktop oldbox:.local/share/applications/
```

> No oldbox o `~/.bashrc` sai logo no começo quando o shell não é interativo, então um
> `ssh oldbox 'jazz …'` não enxerga o `~/.local/bin`. Use `ssh oldbox '~/.local/bin/jazz …'`,
> `ssh -t oldbox 'bash -lc jazz'` ou abra uma sessão interativa (`ssh oldbox` e depois `jazz`).

### install.sh (qualquer Linux/macOS)

```sh
curl -fsSL <url>/install.sh | sh
