package main

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// methods_contract_test.go — portão estático dos assets do cérebro offline
// (docs/research/offline-optimization-study.md, itens 2 e 6).
//
// Os bugs que a suíte real achou em execução eram visíveis nos arquivos:
// uma intenção de escrita cujo único método era de leitura (git_stash), um
// método de risco alto verificado só por "o comando respondeu", um slot que a
// gramática extrai e nenhum método consome. Este teste lê methods/*.json,
// nlu/intents.json e a tabela de frases e falha quando um contrato quebra.
// As violações que já existiam ficam em evals/contracts/known.txt: a lista
// só pode encolher (entrada que deixou de valer também falha o teste).

// mcWriteCaps: capabilities that change the project or the machine.
var mcWriteCaps = map[string]bool{"file_op": true, "text_op": true, "archive_op": true, "perm_op": true, "git_op": true,
	"proc_kill": true, "pkg_cmd": true, "container_op": true, "undo": true, "mkdir_in": true, "save_list": true, "learned_cmd": true}

// mcStepWrites refina a tabela por step: text_op com op=sort e sem destino
// (to/inplace) só despeja o conteúdo ordenado na resposta — brTextSort nem
// abre o arquivo pra escrita. Qualquer outro text_op reescreve o arquivo.
func mcStepWrites(s mhStep) bool {
	if s.Cap == "text_op" && s.In["op"] == "sort" {
		_, to := s.In["to"]
		_, inplace := s.In["inplace"]
		return to || inplace
	}
	return mcWriteCaps[s.Cap]
}

var mcSlotRef = regexp.MustCompile(`\$slot\.([a-zA-Z_][a-zA-Z0-9_]*)`)

func mcViolations(t *testing.T) []string {
	t.Helper()
	lib, err := mhLibrary()
	if err != nil {
		t.Fatal(err)
	}
	m := nuGet()
	if m.loadErr != nil {
		t.Fatal(m.loadErr)
	}
	var out []string
	add := func(f string, a ...string) { out = append(out, f+" "+strings.Join(a, " ")) }

	// what each intent's methods read from the slots
	used := map[string]map[string]bool{}
	for intent, methods := range lib {
		used[intent] = map[string]bool{}
		for _, me := range methods {
			writes, runs := false, false
			for _, s := range me.Steps {
				if brCap(s.Cap) == nil && !strings.HasPrefix(s.Cap, "script:") {
					add("cap-desconhecida", me.Name, s.Cap)
				}
				writes = writes || mcStepWrites(s)
				runs = runs || s.Cap != "report" && s.Cap != "ask_slot" && s.Cap != "say"
				for _, v := range s.In {
					for _, r := range mcSlotRef.FindAllStringSubmatch(v, -1) {
						used[intent][r[1]] = true
					}
					if strings.Contains(v, "$goal") {
						used[intent]["*"] = true // the whole request goes to the step
					}
				}
				if s.Cap == "refill" || s.Cap == "for_each" || s.Cap == "enum" || s.Cap == "need" || s.Cap == "learned_cmd" {
					used[intent]["*"] = true // these read the slot table themselves
				}
				if strings.HasPrefix(s.Out, "slot.") {
					used[intent][strings.TrimPrefix(s.Out, "slot.")] = true
				}
			}
			for _, c := range me.Pre {
				if strings.HasPrefix(c.Fact, "slot.") {
					used[intent][strings.TrimPrefix(c.Fact, "slot.")] = true
				}
			}
			for _, c := range me.Verify {
				for _, v := range []string{c.Arg, c.Arg2} {
					for _, r := range mcSlotRef.FindAllStringSubmatch(v, -1) {
						used[intent][r[1]] = true
					}
				}
			}
			// 1. a method that writes is never "low" risk (low acts without asking)
			if writes && me.Risk == "low" {
				add("escrita-com-risco-baixo", me.Name)
			}
			// 2. a method that runs something has a verifier
			if runs && len(me.Verify) == 0 {
				add("sem-verificador", me.Name)
			}
			if me.Risk != "low" && me.Risk != "med" && me.Risk != "medium" && me.Risk != "high" {
				add("risco-invalido", me.Name, me.Risk)
			}
		}
	}

	// 3. write intent with no write method: an example of the intent carries a
	// write verb, and every method of it is a plain read
	for _, d := range m.data.Intents {
		methods := lib[d.Name]
		if len(methods) == 0 {
			continue
		}
		allRead := true
		for _, me := range methods {
			if !brReadOnlyMethod(me) && !brReportOnly(me) {
				allRead = false
			}
		}
		if allRead {
			for _, ex := range d.Examples {
				if brWriteVerbRe.MatchString(ex) && !brListWordRe.MatchString(ex) {
					add("intencao-de-escrita-so-com-leitura", d.Name)
					break
				}
			}
		}
		// 4. slot the grammar extracts and no method of the intent ever reads:
		// the operand is silently ignored and the answer is sealed anyway
		if used[d.Name]["*"] || d.GoalPassthrough {
			continue
		}
		for slot := range d.Slots {
			if !used[d.Name][slot] {
				add("slot-sem-consumidor", d.Name+"."+slot)
			}
		}
	}

	// 5. phrases: the intent has methods and every slot they set is read
	for _, p := range nuPhraseAll() {
		methods := lib[p.intent]
		if len(methods) == 0 {
			add("frase-sem-metodo", p.id, p.intent)
			continue
		}
		if used[p.intent]["*"] {
			continue
		}
		for _, slot := range p.groups {
			if !used[p.intent][slot] {
				add("frase-slot-sem-consumidor", p.id, p.intent+"."+slot)
			}
		}
		for slot := range p.fixed {
			if !used[p.intent][slot] {
				add("frase-slot-sem-consumidor", p.id, p.intent+"."+slot)
			}
		}
	}
	sort.Strings(out)
	return out
}

func TestMethodContracts(t *testing.T) {
	got := mcViolations(t)
	const path = "evals/contracts/known.txt"
	if os.Getenv("JAZZ_UPDATE_CONTRACTS") == "1" {
		body := "# Violações de contrato conhecidas (methods_contract_test.go). Só pode encolher.\n" +
			"# Cada linha é uma dívida: ou conserta o asset, ou explica aqui por que é exceção.\n" + strings.Join(got, "\n") + "\n"
		if err := os.MkdirAll("evals/contracts", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("%d violações gravadas em %s", len(got), path)
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (gere com JAZZ_UPDATE_CONTRACTS=1 go test -run TestMethodContracts .)", err)
	}
	known := map[string]bool{}
	for _, l := range strings.Split(string(b), "\n") {
		if i := strings.Index(l, "#"); i >= 0 {
			l = l[:i]
		}
		if l = strings.TrimSpace(l); l != "" {
			known[l] = true
		}
	}
	seen := map[string]bool{}
	for _, v := range got {
		seen[v] = true
		if !known[v] {
			t.Errorf("contrato novo quebrado: %s", v)
		}
	}
	for v := range known {
		if !seen[v] {
			t.Errorf("não é mais violação, tire de %s: %s", path, v)
		}
	}
}
