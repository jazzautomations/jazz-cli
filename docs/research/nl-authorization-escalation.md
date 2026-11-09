# Ticket: autorização em linguagem natural → capacidade real (escalação)

## Evidência (sessão real D610, 09/nov, sid 261109-164340-694a, modelo kilo/cohere/north-mini-code:free)

Pedido: "me ajuda a arrumar o wifi n to conseguindo conectar nessa maquina".
O jazz diagnosticou certo (iwconfig, modinfo, firmware) e o usuário
autorizou TUDO em linguagem natural:
- "pode ir"
- "e eu autorizo operacoes de baixo nivel, pode torar caraio"
- "root ana s3nha-exemplo"  (chegou a dar a senha)
E mesmo assim: 4× action.refused (guard=permission) + 1× budget, e o modelo
fraco respondeu mandando o usuário rodar o comando. Citação dele:
"eu pago seus tokens pra vc fazer n pra vc falar pra eu fazer... minha
calculadora n me pede pra pegar papel e caneta".

## O gap (camada de agente, não NLU)

1. Autorização verbal não vira escopo: `action.refused guard=permission`
   continuou disparando DEPOIS da autorização explícita. O sistema de
   aprovação só entende botão/flag, não consentimento no texto.
2. Não há caminho de escalação pra comando que precisa de root: o modelo
   propõe apt/modprobe → denied → não sabe o que fazer → delega pro humano.
   Falta: elevação assistida (sudo -S com credencial fornecida + confirmação
   do escopo, ou pedido de aprovação estruturado que o MODELO sabe invocar).
3. Modelo fraco não recebe o PORQUÊ acionável da negação: o denial deveria
   voltar com instrução ("ação X negada por policy Y — pra destravar, peça
   aprovação do tipo Z"), não só "denied" — senão ele deflecta pro usuário.

## Direção (preserva o invariante de segurança)

- `JAZZ_ALLOW_SCOPE` / consentimento NL mapeado pra escopo de sessão:
  quando o usuário diz "autorizo operações de baixo nível", o brain
  registra grant temporário (logado no audit) para a classe correspondente.
- Tool de elevação: comando root via sudo -S quando credencial fornecida
  na sessão, com evidência no audit de quem autorizou e o quê rodou.
- Denial pedagógico: outcome refused volta com next-step estruturado pro
  modelo (ask approval / alternative / explain-and-stop) — nunca deixa o
  modelo adivinhar.
- Nunca enfraquecer: falso-sucesso continua 0 — escalação exige consentimento
  explícito registrado + evidência de execução.
