package main

import (
	"strings"
)

var (
	// These exact visual markers belong to transcript renderers such as Jazz
	// and Claude Code. A leading tool heading catches single-line scrollback; otherwise requiring
	// both a heading and an exit footer keeps
	// normal reports and discussion about tools out of the confirmation path.
	transcriptToolLine = lazyRx(`(?m)^\s*[●⏺]\s+(Bash|Read|Write|Edit|Grep|Glob|Task|WebFetch|WebSearch|NotebookEdit)\(`)
	transcriptExitLine = lazyRx(`(?m)^\s*⎿\s*exit(?:\s+code)?\s*[:=]?\s*[0-9]+\b`)
)

func looksLikePastedToolTranscript(text string) bool {
	trimmed := strings.TrimSpace(text)
	first := strings.SplitN(trimmed, "\n", 2)[0]
	return transcriptToolLine.MatchString(first) || (transcriptToolLine.MatchString(text) && transcriptExitLine.MatchString(text))
}

func quotePastedToolTranscript(text string) string {
	return grWrapUntrustedUX("transcrição de ferramenta colada pelo usuário", "terminal", text)
}

func confirmPastedToolTranscript(text string, choose func(string, []mnItem, int) int) (string, bool) {
	if !looksLikePastedToolTranscript(text) {
		return text, true
	}
	question := "Este texto parece uma transcrição de saída do Jazz ou de outra ferramenta. Enviar como citação não confiável para análise?"
	items := yesNo("Enviar como citação", "Cancelar")
	if choose == nil || choose(question, items, 1) != 0 {
		return "", false
	}
	return quotePastedToolTranscript(text), true
}

func (a *App) confirmPastedToolTranscript(text string) (string, bool) {
	result, ok := confirmPastedToolTranscript(text, a.ui.Choose)
	if !ok {
		a.ui.Notice("mensagem não enviada")
	}
	return result, ok
}
