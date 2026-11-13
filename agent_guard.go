package main

import (
	"strings"
)

// fakeActionNudge is sent when a final answer claims work that no tool did.
const fakeActionNudge = "You did not call any tool in this turn. Do the work with the tools now; never claim actions you did not perform."

// stallNudge is sent when the reply ends announcing an action it did not take.
const stallNudge = "Your reply ends by announcing a next step, but you made no tool call, so nothing happened. Do it now with the tools. If the task is actually finished, give the final answer instead (what was done and how it was verified)."

// unverifiedWarning is shown when the model insists on a claim without tools.
const unverifiedWarning = "resposta não verificada: o modelo não executou nenhuma ferramenta"

// First-person past-tense action claims. Portuguese preterite forms ending in
// -ei are first person only; English needs an explicit "I"/"we" subject so that
// explanations ("this function creates…", "it was installed") never match.
var (
	acClaimPT = lazyRx(`(?i)(^|[^\p{L}])(n[ãa]o\s+|nunca\s+)?(criei|editei|escrevi|rodei|executei|instalei|apaguei|deletei|removi|corrigi|alterei|modifiquei|atualizei|adicionei|salvei|gerei|compilei|testei|consertei|implementei|mostrei o conte[úu]do)([^\p{L}]|$)`)
	acClaimEN = lazyRx(`(?i)\b(i|we)(\s+have|'ve)?(\s+(just|also|now|already|then|successfully))?\s+(created|wrote|written|edited|ran|executed|installed|fixed|deleted|removed|updated|modified|added|saved|generated|compiled|implemented)\b`)
)

// acClaimsAction reports whether text claims, in the first person and past
// tense, that the assistant did something (created/edited/ran… a thing).
func acClaimsAction(text string) bool {
	if strings.TrimSpace(text) == "" {
		return false
	}
	for _, m := range acClaimPT.FindAllStringSubmatch(text, -1) {
		if m[2] == "" { // "não criei" is an honest negative, not a claim
			return true
		}
	}
	return acClaimEN.MatchString(text)
}

// acRecap: the user asks a question about earlier work ("o que você fez?") and
// tools already ran in this session, so a past-tense recap is legitimate.
func acRecap(input string, msgs []Message) bool {
	if !strings.HasSuffix(strings.TrimSpace(acStripReminders(input)), "?") { // skill/plan reminders are appended after the request
		return false
	}
	for _, m := range msgs {
		if m.Role == "tool" {
			return true
		}
	}
	return false
}

// acAnnounceRE: the tail of a reply announcing the assistant's next action
// (PT/EN), e.g. "Agora vou rodar os testes.", "Let me check the logs:".
var acAnnounceRE = lazyRx(`(?i)(?:^|[.!?:\n]\s*)(?:agora\s+|em seguida\s+|então\s+|entao\s+|primeiro\s+|vou\s+agora\s+)?(?:vou|irei|vamos)\s+(?:agora\s+|então\s+)?(?:rodar|executar|criar|editar|verificar|checar|testar|ler|abrir|olhar|corrigir|ajustar|implementar|adicionar|instalar|atualizar|escrever|procurar|buscar|conferir|investigar|analisar|aplicar|fazer|mudar|alterar|remover|compilar)\b[^.!?\n]{0,160}[.:!…]?\s*$|(?:^|[.!?:\n]\s*)(?:let me|i'll|i will|now i'll|next,? i'll|now let me|i'm going to)\s+(?:now\s+)?(?:run|execute|create|edit|check|verify|test|read|open|look|fix|update|implement|add|install|write|search|apply|make|change|remove|build|investigate|inspect)\b[^.!?\n]{0,160}[.:!…]?\s*$`)

// acAnnouncesAction: a short reply whose last sentence announces an action.
func acAnnouncesAction(text string) bool {
	t := strings.TrimSpace(text)
	if t == "" || len(t) > 700 || strings.HasSuffix(t, "?") {
		return false
	}
	return acAnnounceRE.MatchString(t)
}
