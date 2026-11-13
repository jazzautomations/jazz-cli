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

// Recap requests without a '?' ("me fala o que vc corrigiu", "resume o que
// mudou", "lista o que você fez"): an ask cue plus a reference to past work.
// A request that also orders new work ("corrige o que vc fez e me fala") is
// not a recap: claims there must still come from tools.
var (
	acRecapAskRE  = lazyRx(`(?i)(^|[^\p{L}])(resum\p{L}*|recap\p{L}*|summar\p{L}*|list[ae]r?|explic\p{L}*|mostr[ae]|cont[ae]|fal[ae]|diz|diga|tell me|list|show me|walk me through|what did|what have)([^\p{L}]|$)`)
	acRecapPastRE = lazyRx(`(?i)(^|[^\p{L}])(fez|fizemos|feito|fizeram|mudou|mudamos|mudan[çc]as?|corrigiu|corrigimos|corre[çc][õo]es|arrumou|arrumamos|alterou|altera[çc][õo]es|consertou|implementou|criou|rodou|testou|did|done|changed|changes|fixed|made)([^\p{L}]|$)`)
	acRecapDoRE   = lazyRx(`(?i)(^|[^\p{L}])(corrig[ae]|corrij[ae]|consert[ae]|arrum[ae]|faz|fa[çc]a|cri[ae]|rod[ae]|implement[ae]|adicion[ae]|mud[ae]|alter[ae]|apag[ae]|remov[ae]|instal[ae]|edit[ae]|escrev[ae]|atualiz[ae])([^\p{L}]|$)|^\s*(please\s+)?(fix|run|create|add|implement|delete|install|write|edit|update)\b`)
)

// acRecapAsk: the request (reminders stripped) asks about earlier work.
func acRecapAsk(input string) bool {
	q := strings.TrimSpace(acStripReminders(input)) // skill/plan reminders are appended after the request
	if strings.HasSuffix(q, "?") {
		return true
	}
	return acRecapAskRE.MatchString(q) && acRecapPastRE.MatchString(q) && !acRecapDoRE.MatchString(q)
}

// acRecap: the user asks about earlier work ("o que você fez?", "me fala o que
// vc corrigiu") and tools already ran in an earlier turn of this session, so a
// past-tense recap is legitimate. The anti-fake guard only asks when this turn
// made no call, so any tool message in msgs belongs to an earlier turn.
func acRecap(input string, msgs []Message) bool {
	if !acRecapAsk(input) {
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
