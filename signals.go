package main

// signals.go: ledger de sinais do asset-forge — o registro append-only e
// zero-token de onde o cérebro offline ERROU (miss, unknown_intent,
// verifier_missing, new_method) ou o dono CORRIGIU (correction, cmd_fixed).
// Em offline-time o Fable destila estes sinais em assets: é a base da
// realimentação "miss/correção -> asset".
//
// Dados: dataDir()/signals.jsonl, uma linha JSON por sinal. Append O(1) sob
// mutex de processo + flock (o mesmo padrão de scores.go/ratelimit.go, então
// processos jazz concorrentes nunca entrelaçam linhas); leitura por streaming
// via bufio.Scanner (até 1 MiB por linha), com linhas corrompidas puladas sem
// abortar. Zero dependência externa (só stdlib).

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Signal é uma entrada do ledger: um ponto onde a resolução automática falhou
// ou foi corrigida, com o contexto necessário para a forja aprender com ele.
type Signal struct {
	Ts       int64             `json:"ts"`                 // unix segundos; preenchido no append se zero
	Kind     string            `json:"kind"`               // miss|correction|cmd_fixed|verifier_missing|unknown_intent|new_method
	Class    string            `json:"class"`              // classe da tarefa (edit, cmd, nav, ...)
	RepoSig  string            `json:"repo_sig,omitempty"` // assinatura do repo (opcional)
	Text     string            `json:"text"`               // o pedido do usuário, como veio
	Chosen   string            `json:"chosen,omitempty"`   // o que ele escolheu/corrigiu (opcional)
	Verified bool              `json:"verified"`           // a resolução foi verificada?
	Model    string            `json:"model,omitempty"`    // modelo que resolveu depois, se houve
	Extra    map[string]string `json:"extra,omitempty"`    // pares soltos (causa do verificador, tool, ...)
}

// sigPath devolve o caminho do ledger. É uma var para os testes apontarem o
// ledger para um t.TempDir() sem depender de JAZZ_HOME.
var sigPath = func() string {
	return filepath.Join(dataDir(), "signals.jsonl")
}

// sigMu serializa os appends dentro deste processo (o flock do pvFlock cobre
// os demais processos jazz).
var sigMu sync.Mutex

// sigAppend acrescenta s ao ledger em O(1): uma linha JSON por sinal, escrita
// com O_APPEND|O_CREATE sob mutex de processo + flock, então nem goroutines
// nem processos concorrentes corrompem o arquivo. Preenche s.Ts com a hora
// atual quando vem zero.
func sigAppend(s Signal) error {
	if s.Ts == 0 {
		s.Ts = time.Now().Unix()
	}
	line, err := json.Marshal(s)
	if err != nil {
		return err
	}
	p := sigPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	sigMu.Lock()
	defer sigMu.Unlock()
	unlock, err := pvFlock(p+".lock", 2*time.Second)
	if err != nil {
		return err
	}
	defer unlock()
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(line, '\n'))
	return err
}

// sigRead varre o ledger em streaming e devolve os sinais que passam em
// filter (nil = todos). Linhas vazias ou que não decodificam como Signal são
// puladas sem abortar — um byte corrompido nunca derruba a leitura inteira.
// Arquivo inexistente não é erro: devolve lista vazia.
func sigRead(filter func(Signal) bool) ([]Signal, error) {
	f, err := os.Open(sigPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var out []Signal
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		var s Signal
		if json.Unmarshal(sc.Bytes(), &s) != nil {
			continue // linha lixo/corrompida: pula sem abortar
		}
		if filter == nil || filter(s) {
			out = append(out, s)
		}
	}
	return out, sc.Err()
}

// sigReadSince devolve os sinais com Ts >= ts (unix segundos).
func sigReadSince(ts int64) ([]Signal, error) {
	return sigRead(func(s Signal) bool { return s.Ts >= ts })
}

// sigByClass devolve os sinais da classe de tarefa class.
func sigByClass(class string) ([]Signal, error) {
	return sigRead(func(s Signal) bool { return s.Class == class })
}

// sigCount devolve quantos sinais existem por Kind (ex.: miss: 12).
func sigCount() (map[string]int, error) {
	sigs, err := sigRead(nil)
	if err != nil {
		return nil, err
	}
	out := make(map[string]int, 8)
	for _, s := range sigs {
		out[s.Kind]++
	}
	return out, nil
}
