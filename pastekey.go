package main

import "strings"

// pastekey.go — a chave colada chega limpa.
//
// Um terminal com "bracketed paste" envolve o texto colado em ESC[200~ e
// ESC[201~. O leitor de `jazz key` descartava o byte ESC (controle) ANTES de
// tirar os marcadores, e o que sobrava — "[200~" na frente e "[201~" atrás —
// era gravado como parte da chave. O provedor respondia "Please pass a valid
// API key" e o `jazz doctor` só dizia que a chave "não parece" do provedor.
// Achado em 2026-11-10 numa chave salva assim.

// pkPasteRe: marcador de colagem inteiro ou o resto dele sem o ESC, na ponta
// ou no meio. "~" e "[" não aparecem em chave de API de nenhum provedor
// suportado, então o resto do marcador nunca é parte de uma chave válida.
var pkPasteRe = lazyRx(`\x1b?\[?20[01]~`)

// pkCleanKey removes paste markers, control characters and surrounding
// whitespace or quotes from a pasted credential.
func pkCleanKey(s string) string {
	s = pkPasteRe.ReplaceAllString(s, "")
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if len(s) >= 2 && (s[0] == '"' && s[len(s)-1] == '"' || s[0] == '\'' && s[len(s)-1] == '\'') {
		s = strings.TrimSpace(s[1 : len(s)-1])
	}
	return s
}

// pkLooksPasted: a stored key still carries a paste marker (saved by a
// version before the fix). The key is used cleaned; doctor says so.
func pkLooksPasted(s string) bool { return pkPasteRe.MatchString(s) }
