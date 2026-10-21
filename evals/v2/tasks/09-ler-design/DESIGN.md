# ponto — decisões de projeto

## 1. Escopo

Apuração do ponto dos funcionários da oficina. Entrada: arquivos texto com uma marcação
por linha (`AAAA-MM-DD HH:MM HH:MM`, entrada e saída). Um dia pode ter várias marcações.

## 2. Convenções de código

- Tempo é sempre **minutos inteiros** (`int`). Nada de `float` nem `timedelta` na API pública.
- Uma marcação que passa da meia-noite (entrada 22:00, saída 02:00) pertence ao dia da **entrada**.
- Funções de cálculo recebem a lista de `Registro` e não leem arquivo.
- `datas.semana_iso()` (segunda a domingo) é **só para exibição** nos relatórios.

## 3. Horas extras (acordo coletivo 2026, cláusula 7)

As regras abaixo vêm do acordo com o sindicato e **não** são as da CLT padrão. Não "corrija".

1. **Semana de apuração: de quarta-feira a terça-feira.** A semana que contém uma data é a
   que começa na quarta-feira anterior ou igual a ela. (Não use `semana_iso()` aqui.)
2. Jornada semanal: **2.400 minutos** (40 h), somando só o que foi trabalhado de **segunda a sábado**.
3. **Domingo:** todo minuto trabalhado no domingo é extra e conta **em dobro**
   (60 min no domingo = 120 min de extra). Minuto de domingo não entra na jornada.
4. Extra da semana = `max(0, minutos de segunda a sábado − 2400) + 2 × minutos de domingo`.
5. **Tolerância:** se o extra da semana der **10 minutos ou menos**, vale 0. Acima de 10, conta inteiro.
6. O resultado é em minutos inteiros.
