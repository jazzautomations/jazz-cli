package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// brain_honest.go — o selo cobre o VERBO do pedido, não só os operandos.
//
// A sonda de 89 pedidos reais (docs/research/offline-realworld-probe.md,
// 2026-11-09) achou sete "feito e verificado" que não cumpriam o pedido. Três
// causas ficam fechadas aqui, antes de qualquer método rodar:
//
//  1. Pergunta nunca dispara intenção que muda coisa. "qual foi o último
//     commit" era lido como git_commit (a regra é a palavra "commit") e
//     commitava tudo com a mensagem "último".
//  2. Pedido de escrita cuja única resposta offline é um comando de leitura
//     não roda a leitura pra depois selar verde. "guarda minhas mudanças no
//     stash" rodava `git stash list`; "cria uma branch chamada teste" rodava
//     `git branch`.
//  3. Trabalho que precisa de modelo não vira `create` de um arquivo vazio.
//     "escreve um teste pra função Soma" criava um arquivo chamado Soma.
//
// Nos três casos a resposta é um miss honesto, sem efeito colateral: diz o que
// foi lido, por que não agiu e o que dá pra fazer agora.

// brQuestionRe: a frase começa com palavra interrogativa. Um "?" no fim não
// basta ("commita isso pra mim?" é pedido educado, não pergunta).
var brQuestionRe = lazyRx(`(?i)^\s*(?:e\s+|ent[aã]o\s+|mas\s+|a[ií]\s+)?(?:qual|quais|quem|quando|onde|cad[eê]|quant[oa]s?|por\s*qu[eê]|porqu[eê]|pq|como|o\s+que|oq|what|which|who|whose|when|where|why|how|is|are|was|were|does|do|did|tem|existe|h[aá])(?:[\s?,.!:]|$)`) // RE2: \b é ASCII e falha depois de "cadê"

func brIsQuestion(text string) bool { return brQuestionRe.MatchString(text) }

// brWriteVerbRe: verbo de escrita no imperativo/infinitivo (pt e en). Formas
// de particípio ("criadas", "changed") ficam de fora de propósito: "lista as
// branches criadas hoje" é leitura.
var brWriteVerbRe = lazyRx(`(?i)(?:^|[\s,;:])(cria|crie|criar|create|make|guarda|guarde|guardar|salva|salve|salvar|save|esconde|esconder|aplica|aplicar|apply|pop|adiciona|adicionar|add|muda|mudar|troca|trocar|switch|altera|alterar|change|apaga|apagar|deleta|deletar|delete|remove|remover|renomeia|renomear|rename|restaura|restaurar|restore|descarta|descartar|drop|discard)\b`)

// brListWordRe: o pedido diz explicitamente que quer ver, não fazer.
var brListWordRe = lazyRx(`(?i)\b(lista|listar|liste|list|mostra|mostrar|mostre|show|ver|veja|quais|qual|tem|what|which)\b`)

// brBareWrite são intenções cujo nome já é o verbo de escrita: "git stash" ou
// "stash" sozinho quer dizer guardar, e o único método offline só lista.
var brBareWrite = map[string]bool{"git_stash": true}

// brFirstMethod is the method brExecute would pick first for in (the same
// applicability walk as brIntentRisk).
func brFirstMethod(env *brEnv, in Intent) (mhMethod, bool) {
	lib, _ := mhLibrary()
	methods := lib[in.Name]
	if len(methods) == 0 {
		return mhMethod{}, false
	}
	p := env.Profile
	if p == nil {
		p = epGet(false)
	}
	probe := &brCtx{env: env, in: in, task: &TaskState{Slots: in.Slots, Facts: map[string]string{}}}
	probe.proj = fpDetect(env.Root)
	facts := brFacts(probe, p)
	for _, m := range methods {
		if mhApplicable(m, facts) {
			return m, true
		}
	}
	return mhMethod{}, false
}

// brReadOnlyMethod: every step is a plain read command.
func brReadOnlyMethod(m mhMethod) bool {
	if len(m.Steps) == 0 {
		return false
	}
	for _, s := range m.Steps {
		if s.Cap != "read_cmd" {
			return false
		}
	}
	return true
}

// brMutates: the reading would change the project or the machine.
func brMutates(in Intent, risk cbRisk) bool { return risk >= cbHigh || in.Name == "create" }

// brVerbGuard returns the honest-miss text (and its MissWhy) when acting on
// in would not be what text asked for; "" = go ahead.
func brVerbGuard(env *brEnv, text string, in Intent, risk cbRisk) (why, kind string) {
	desc := nuGet().descOf(in.Name)
	if brIsQuestion(text) && brMutates(in, risk) {
		return fmt.Sprintf("isso é uma pergunta, e a leitura que eu tenho (%s — %s) muda coisas. não vou agir numa pergunta: nada foi alterado. se era pra fazer, peça no imperativo; se era pra saber, reformule (ex.: \"mostra o git log\")", in.Name, desc), "question_mutation"
	}
	if in.Name == "create" {
		if w, ok := brNeedsModel(text); ok {
			return w, "needs_model"
		}
	}
	if m, ok := brFirstMethod(env, in); ok && brReadOnlyMethod(m) {
		verb := strings.TrimSpace(brWriteVerbRe.FindString(text))
		verb = strings.TrimLeft(verb, ",;: ")
		bare := brBareWrite[in.Name] && !brListWordRe.MatchString(text)
		if verb != "" || bare {
			if verb == "" {
				verb = in.Name
			}
			show := ""
			if len(m.Cmds) > 0 {
				show = fmt.Sprintf(" o que eu sei mostrar agora: `%s` (peça \"lista\"/\"mostra\").", m.Cmds[0])
			}
			return fmt.Sprintf("o pedido é de escrita (%q) e, sem modelo, de %s eu só faço a parte de leitura. não fiz nada e não vou fingir que fiz.%s pra fazer de verdade, use um modelo (rode sem --offline, ou /online)", verb, in.Name, show), "write_unsupported"
		}
	}
	return "", ""
}

// brNoTestsRan: the runner said it ran nothing. Go prints "[no tests to run]"
// on every "ok" line when -run matches no test; pytest prints "no tests ran".
func brNoTestsRan(out string) bool {
	if strings.Contains(out, "no tests ran") || strings.Contains(out, "collected 0 items") {
		return true
	}
	ok, empty := 0, 0
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "ok ") || strings.HasPrefix(l, "ok\t") {
			ok++
			if strings.Contains(l, "[no tests to run]") {
				empty++
			}
		}
	}
	return ok > 0 && ok == empty
}

// brBareFile resolves a bare file name (no directory) that does not exist at
// root to the ONE project file with that base name. Zero or several matches:
// ok=false, and the caller keeps the name as given (the step fails honestly).
func brBareFile(root, name string) (rel string, ok bool) {
	if name == "" || strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
		return "", false
	}
	if _, err := os.Lstat(filepath.Join(root, name)); err == nil {
		return "", false
	}
	found := ""
	for _, f := range niListWalk(root).files {
		if filepath.Base(f) == name {
			if found != "" {
				return "", false
			}
			found = f
		}
	}
	return found, found != ""
}
