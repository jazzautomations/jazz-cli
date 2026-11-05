package main

// brain_smalltalk.go — the offline brain's manners (Robô Ed style).
//
// Two things a person expects from an assistant even with no model:
//   1. small talk answered at once: "oi", "valeu", "tudo bem?", "tchau" get a short human reply plus what jazz can do offline
//      — never the "não entendi" menu;
//   2. an honest "this needs a model" for creative code work (refactor,
//      implement, document, migrate…): say WHY the offline brain cannot,
//      what it CAN do right now, and how to turn a model on — instead of a
//      generic menu that reads as a malfunction.
// Neither ever claims a task was done: small talk is not a task, and the
// needs-model answer stays a miss (handled=false) so the model, when there
// is one, still takes the request.

import (
	"regexp"
	"strings"
)

var (
	stGreet  = regexp.MustCompile(`^(oi+|ola|olá|opa|eai|e ai|e aí|eaí|fala|salve|bom dia|boa tarde|boa noite|hello|hi|hey|yo)( (jazz|mano|cara|ai|aí|galera))?$`)
	stHowAre = regexp.MustCompile(`^((oi+|ola|olá|opa|eai|e ai|e aí|fala|salve|bom dia|boa tarde|boa noite|hello|hi|hey) )?(tudo bem|tudo bom|tudo certo|como vai|como voce esta|como você está|beleza|suave|how are you)( (com voce|com você|ai|aí))?$`)
	stThanks = regexp.MustCompile(`^(valeu|vlw|obrigad[oa]|brigad[oa]|thanks|thank you|thx|show|top|massa|perfeito|boa)( (mano|cara|jazz|demais|mesmo))?$`)
	stBye    = regexp.MustCompile(`^(tchau|falou|flw|ate mais|até mais|ate logo|até logo|bye|see you)( (mano|jazz))?$`)
	stModel  = regexp.MustCompile(`\b(refator\w*|implement\w*|document\w*|reescrev\w*|migr\w*|otimiz\w*|traduz\w*|generics|escreve uma? (funcao|função|classe|teste|api|script)|cria uma? (funcao|função|classe|api|feature)|adiciona suporte|refactor\w*|rewrite|write a (function|class|test))\b`)
)

// stOffline is the short "what I do with no model" line.
const stOffline = "sem modelo eu resolvo na hora: portas abertas, uso de disco e memória, git (status, log, quem mexeu, conflitos, branches), TODOs, arquivos recentes, achar arquivo ou símbolo, ler config, rodar os testes — digite \"ajuda\" pra ver tudo"

// brSmallTalk answers conversation that is not a task. ok=false: not small talk.
func brSmallTalk(text string) (string, bool) {
	t := strings.ToLower(strings.TrimSpace(strings.Trim(text, "!?.,;: ")))
	t = strings.Join(strings.Fields(strings.NewReplacer(",", " ", "!", " ", "?", " ").Replace(t)), " ")
	if t == "" || len(t) > 60 {
		return "", false
	}
	switch {
	case stThanks.MatchString(t):
		return "de nada! se precisar de mais alguma coisa é só pedir.", true
	case stBye.MatchString(t):
		return "falou! até a próxima.", true
	case stHowAre.MatchString(t):
		return "tudo certo por aqui, pronto pra trabalhar. " + stOffline + ".", true
	case stGreet.MatchString(t):
		return "oi! " + stOffline + ".", true
	}
	return "", false
}

// brNeedsModel explains a creative request the offline brain cannot do.
func brNeedsModel(text string) (string, bool) {
	t := strings.ToLower(text)
	m := stModel.FindString(t)
	if m == "" {
		return "", false
	}
	return "isso (" + m + ") é trabalho criativo de código: precisa de um modelo, e agora estou sem modelo. " +
		stOffline + ". pra usar um modelo: rode sem --offline (ou /online), ou configure um com `jazz login chatgpt`, `jazz key <provedor>` ou `jazz omni setup` (tem modelos grátis).", true
}
