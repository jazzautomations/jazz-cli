package main

// lang.go: which language the user writes in. Weak models answered a
// Portuguese request in English 31–40% of the time (v0.13 sessions) with an
// English-only system prompt; a Portuguese request gets one Portuguese line at
// the end of the system prompt, added before the first request of the
// session so the provider's prompt cache is not broken later.

import (
	"strings"
	"unicode"
)

// langPTNote is appended to the system prompt when the user writes Portuguese.
const langPTNote = "\n\nResponda em português do Brasil, como um colega: direto, sem relatório."

var (
	langPTWords = wordSet(`não nao que pra pro para com uma um é tá ta isso esse essa este esta meu minha você voce vc aqui ali porque porquê como onde quando tem faz fazer arruma conserta corrige roda mostra cadê cade ver veja olha preciso quero pode mano oi valeu obrigado obrigada ainda já ja também tambem agora depois sim nada tudo muito mais mas pelo pela nos nas dos das no na do da os as se ou foi está esta estao estão ser sobre sem seu sua ele ela`)
	langENWords = wordSet(`the is are was were what how why where when to of and please can could would should you your it this that with fix make add run show from for on in at be have has do does not my me we there which`)
)

func wordSet(s string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(s) {
		m[w] = true
	}
	return m
}

// langIsPT: the text reads as Portuguese (stop words, ã/õ/ç), not English.
func langIsPT(s string) bool {
	s = strings.ToLower(acStripReminders(s))
	pt, en := 0, 0
	for _, w := range strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) }) {
		switch {
		case langPTWords[w]:
			pt++
		case langENWords[w]:
			en++
		}
		if strings.ContainsAny(w, "ãõçê") {
			pt++
		}
	}
	return pt > 0 && pt >= en
}

// langNote adds langPTNote to the system prompt on the first turn of a
// session whose first message is Portuguese. Later turns never touch the
// system prompt (it stays byte-identical for the prompt cache).
func (a *Agent) langNote(input string) {
	if a.sub || a.System == "" || len(a.Msgs) > 0 || strings.HasSuffix(a.System, langPTNote) || !langIsPT(input) {
		return
	}
	a.System += langPTNote
}
