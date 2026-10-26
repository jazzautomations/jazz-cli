package main

// askfacts.go: ask_user questions the project itself can answer never reach
// the user. "Which branch?", "which test runner / package manager?", "does
// file X exist?" are facts on disk; asking them costs the user a round trip
// (the owner's "dá muita volta"). A question is answered here only when it
// clearly asks for one of those facts and, when it has options, one option
// names the fact; anything about preferences or intent still goes to the
// user. The answer says where it came from, so the model can tell.
// JAZZ_ASK_FACTS=off sends every question to the user.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// afFact is one locally known answer.
type afFact struct {
	answer string // what goes back to the model (an option label when there are options)
	source string // how it was found ("git", "go.mod", "stat")
}

var (
	afBranchRe = lazyRx(`(?i)\b(branch|ramo)\b`)
	afTestRe   = lazyRx(`(?i)(test runner|framework de teste|comando de teste|rodar (os )?testes|roda(r)? os testes|run (the )?tests|test command|testing framework|qual (é o )?(test|teste))`)
	afPkgRe    = lazyRx(`(?i)(gerenciador de pacotes|package manager|\bnpm\b.*\b(pnpm|yarn|bun)\b|\b(pnpm|yarn|bun)\b.*\bnpm\b)`)
	afExistsRe = lazyRx(`(?i)(existe|exists|já tem|ja tem|there is|is there|tem um arquivo|tem o arquivo)`)
	afPathRe   = lazyRx("[`'\"]?([\\w./-]*[\\w-]+\\.[A-Za-z0-9]{1,8}|[\\w.-]+/[\\w./-]+)[`'\"]?")
	afPrefRe   = lazyRx(`(?i)(prefere|preferir|gostaria|quer que|devo|should i|do you want|would you like|which (one )?(do )?you|você quer|voce quer|qual (você|voce)|melhor)`)
)

// askFromFacts answers q from the project at root when it can.
func askFromFacts(root string, q askArgs) (afFact, bool) {
	if olOffWord(os.Getenv("JAZZ_ASK_FACTS")) {
		return afFact{}, false
	}
	text := q.Question
	if afPrefRe.MatchString(text) && !afBranchRe.MatchString(text) {
		return afFact{}, false // a preference: only the user knows
	}
	var f afFact
	ok := false
	switch {
	case afBranchRe.MatchString(text) && !strings.Contains(strings.ToLower(text), "criar") && !strings.Contains(strings.ToLower(text), "create") && !strings.Contains(strings.ToLower(text), "nova"):
		b := strings.TrimSpace(gitOut(root, "symbolic-ref", "--short", "-q", "HEAD")) // also before the first commit
		if b == "" {
			b = strings.TrimSpace(gitOut(root, "rev-parse", "--abbrev-ref", "HEAD"))
		}
		if b != "" && b != "HEAD" {
			f, ok = afFact{answer: b, source: "git (branch atual)"}, true
		}
	case afTestRe.MatchString(text):
		if c, src := afTestCommand(root); c != "" {
			f, ok = afFact{answer: c, source: src}, true
		}
	case afPkgRe.MatchString(text):
		if c, src := afPackageManager(root); c != "" {
			f, ok = afFact{answer: c, source: src}, true
		}
	case afExistsRe.MatchString(text):
		if m := afPathRe.FindStringSubmatch(text); m != nil {
			p := filepath.Clean(m[1])
			if !filepath.IsAbs(p) && !strings.HasPrefix(p, "..") {
				if st, err := os.Stat(filepath.Join(root, p)); err == nil {
					kind := "o arquivo"
					if st.IsDir() {
						kind = "a pasta"
					}
					f, ok = afFact{answer: "sim, " + kind + " " + p + " existe", source: "stat"}, true
				} else if os.IsNotExist(err) {
					f, ok = afFact{answer: "não, " + p + " não existe", source: "stat"}, true
				}
			}
		}
	}
	if !ok {
		return afFact{}, false
	}
	if len(q.Options) == 0 {
		if !q.free() {
			return afFact{}, false
		}
		return f, true
	}
	// with options, the fact must name exactly one of them
	low := strings.ToLower(f.answer)
	for i, o := range q.Options {
		if strings.EqualFold(strings.TrimSpace(o.Label), f.answer) {
			f.answer = q.Options[i].Label
			return f, true
		}
	}
	hit := -1
	for i, o := range q.Options {
		l := strings.ToLower(strings.TrimSpace(o.Label))
		if l == "" {
			continue
		}
		if l == low || strings.Contains(low, l) || strings.Contains(l, low) || afYesNo(low, l) {
			if hit >= 0 {
				return afFact{}, false // two options match: ask
			}
			hit = i
		}
	}
	if hit < 0 {
		if q.free() {
			return f, true
		}
		return afFact{}, false
	}
	f.answer = q.Options[hit].Label
	return f, true
}

// afYesNo matches "sim, ..."/"não, ..." facts to yes/no option labels.
func afYesNo(fact, label string) bool {
	yes := strings.HasPrefix(fact, "sim")
	switch label {
	case "sim", "yes", "s", "y":
		return yes
	case "não", "nao", "no", "n":
		return strings.HasPrefix(fact, "não")
	}
	return false
}

// afTestCommand is the project's test command, from its build files.
func afTestCommand(root string) (cmd, source string) {
	has := func(p string) bool { _, err := os.Stat(filepath.Join(root, p)); return err == nil }
	if b, err := os.ReadFile(filepath.Join(root, "package.json")); err == nil {
		var pj struct {
			Scripts map[string]string `json:"scripts"`
		}
		if json.Unmarshal(b, &pj) == nil && strings.TrimSpace(pj.Scripts["test"]) != "" && !strings.Contains(pj.Scripts["test"], "no test specified") {
			pm, _ := afPackageManager(root)
			if pm == "" {
				pm = "npm"
			}
			return pm + " test", "package.json (scripts.test: " + oneLine(pj.Scripts["test"], 60) + ")"
		}
	}
	switch {
	case has("go.mod"):
		return "go test ./...", "go.mod"
	case has("Cargo.toml"):
		return "cargo test", "Cargo.toml"
	case has("pytest.ini") || has("conftest.py") || has("pyproject.toml") && afFileHas(filepath.Join(root, "pyproject.toml"), "pytest"):
		return "pytest", "configuração do pytest"
	case has("pom.xml"):
		return "mvn test", "pom.xml"
	case has("build.gradle") || has("build.gradle.kts"):
		return "./gradlew test", "build.gradle"
	case has("Makefile") && afFileHas(filepath.Join(root, "Makefile"), "\ntest:"):
		return "make test", "Makefile (alvo test)"
	case has("mix.exs"):
		return "mix test", "mix.exs"
	}
	return "", ""
}

// afPackageManager is the JS package manager, from the lockfile.
func afPackageManager(root string) (string, string) {
	for _, c := range []struct{ file, pm string }{{"pnpm-lock.yaml", "pnpm"}, {"yarn.lock", "yarn"}, {"bun.lockb", "bun"}, {"bun.lock", "bun"}, {"package-lock.json", "npm"}} {
		if _, err := os.Stat(filepath.Join(root, c.file)); err == nil {
			return c.pm, c.file
		}
	}
	return "", ""
}

func afFileHas(p, s string) bool {
	b, err := os.ReadFile(p)
	return err == nil && strings.Contains("\n"+string(b), s)
}
