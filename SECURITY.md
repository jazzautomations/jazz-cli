# Política de segurança

## O que o jazz pode fazer na sua máquina

O jazz executa comandos de shell e edita arquivos escolhidos por um modelo de linguagem. Leia antes de usar:

- No modo `auto` (padrão em pastas que você confiou), comandos não marcados como perigosos rodam sem perguntar. Em pastas desconhecidas o jazz pergunta antes de confiar.
- Conteúdo de arquivos, saídas de comandos, páginas web e respostas de servidores MCP são tratados como dados, não como instruções, mas nenhum filtro é perfeito. Não aponte o jazz para repositórios em que você não confia sem o modo `ask`.
- Chaves de API ficam em `~/.config/jazz-cli/auth.json` (modo 0600). Elas são removidas do ambiente dos comandos e redigidas dos logs e sessões. Com o sandbox do sistema ativo (abaixo), nenhum comando consegue ler esse arquivo; sem ele, um comando malicioso pode ler qualquer arquivo do seu usuário.
- Provedores gratuitos podem usar seus prompts para treinamento. O jazz avisa uma vez; a escolha é sua.
- Não há telemetria: o jazz só fala com os provedores de modelo que você configurou, com servidores MCP que você cadastrou e, no `jazz update`, com a URL de releases.

## Sandbox do sistema (`--sandbox`)

Tudo que o jazz executa (shell, jobs em segundo plano, hooks, servidores MCP stdio, scripts) roda dentro de um sandbox do sistema operacional, por baixo das guardas do jazz:

- **macOS**: `sandbox-exec` (verificado). **Linux**: `bubblewrap` (`sudo apt install bubblewrap`; não verificado em máquina real ainda). **Windows**: só limites de job object (processos, área de transferência, UI), sem isolamento de arquivos.
- Dentro dele, os comandos não leem sua home (exceto a pasta do projeto e caches de ferramentas como Go, npm e Cargo), nem `~/.ssh`, `~/.aws`, `~/.gnupg`, chaveiros, perfis de navegador ou a config do jazz, mesmo que o projeto seja a sua home. Gravar só no projeto, numa pasta temporária própria e nos caches de pacote.
- `--sandbox auto` (padrão): usa o sandbox se houver; se não houver, roda sem e o `jazz doctor` avisa. `--sandbox strict`: recusa rodar sem sandbox e corta a rede dos comandos (só loopback). `--sandbox off`: desliga. Também `sandbox` / `sandbox_net` na config e `JAZZ_SANDBOX` / `JAZZ_SANDBOX_NET`.
- O sandbox não cobre as ferramentas internas de leitura e edição de arquivos nem o `fetch` (rodam dentro do jazz, protegidas pela cerca do projeto). No modo `auto` os comandos ainda têm rede: exfiltrar algo que eles conseguem ler (o próprio projeto, arquivos fora da home) continua possível. Para repositório desconhecido use `--sandbox strict` e o modo `ask`.
- Teste: `jazz doctor` (linha "sandbox"; `jazz doctor --sandbox-probe` lista o que o sandbox conseguiu ler, `-v` mostra o argv) e `jazz --sandbox strict -p 'echo ok'`.

## Auditoria (`jazz audit`)

O jazz mantém um log de auditoria só de acréscimo, encadeado por hash, em `~/.local/share/jazz-cli/audit/audit.jsonl`: a configuração em vigor a cada início (e toda mudança do `config.json`, por hash), decisões de confiança, toda ação bloqueada, negada ou liberada por confirmação, toda escrita/edição/remoção de arquivo (caminho e hash antes/depois, sessão e passo), todo comando de shell (hash e os primeiros 200 caracteres, redigidos), o primeiro acesso de rede a cada host e as atualizações aplicadas ou recusadas (com o estado da assinatura). Nenhum conteúdo de arquivo ou prompt entra nele.

- Verificar: `jazz audit --verify` (código 0 = cadeia íntegra; 1 = registro alterado, removido ou fora de ordem, com a linha).
- Ler/exportar: `jazz audit [--since 24h] [--kind file] [--json]`.
- Retenção: nunca apagado pelo jazz; acima de 16 MB é rotacionado sem quebrar a cadeia.
- Limite: protege contra edição pontual, não contra quem reescreve a cadeia inteira com acesso à sua home; exporte o último hash para fora da máquina se precisar dessa garantia.

Detalhes, formato e o rastreio de mudanças por arquivo (`jazz changes`): [docs/governance.md](docs/governance.md).

## Versões suportadas

Apenas a última versão publicada recebe correções.

## Relatando uma vulnerabilidade

Abra um *security advisory* privado no GitHub do projeto ou escreva para o e-mail indicado no perfil da organização. Descreva o cenário e, se possível, um comando ou arquivo que reproduza o problema. Respondemos em até 7 dias.
