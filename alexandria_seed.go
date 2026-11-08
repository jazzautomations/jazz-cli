package main

// alexandria_seed.go — os procedimentos-semente da Alexandria.
//
// Cada um é a versão causal de um método embutido (methods/*.json): mesmo
// nome, mesmos passos e verificadores — mais o que o método não escrevia:
// objetivo, parâmetros tipados, dependências, efeitos colaterais, limites de
// capacidade, testes negativos (refutes) e casos de falso sucesso
// (false_ok). Nascem "active" com Wins=axMinWins: a semente é curadoria,
// não aprendizado — a contagem real começa a andar com o uso (axUse).

func axSeedProc(name, family, goal, intent, risk string, cost int) axProcedure {
	return axProcedure{
		ID: "ax-seed-" + name, Name: name, Family: family,
		Goal: goal, Intent: intent, Keys: pcUniq(pcTerms(goal)),
		Risk: risk, CostMS: cost, State: "active",
		Prov: axProv{Source: "seed", Method: name, Wins: axMinWins},
	}
}

// axSeeds devolve os procedimentos-semente. Created/Updated ficam zerados:
// a semente é atemporal — o relógio começa a andar no primeiro uso real.
func axSeeds() []axProcedure {
	var out []axProcedure

	// ---------- família fs ----------

	p := axSeedProc("find.symbol", "fs",
		"localizar a definição de um símbolo do código e contar onde ele é usado",
		"find", "low", 600)
	p.Params = []axParam{{Name: "query", Type: "symbol", Required: true, Desc: "o símbolo a procurar"}}
	p.Pre = []mhCond{{Fact: "slot.query", Op: "set"}}
	p.Deps = []string{"bin.python3"}
	p.Steps = []mhStep{{Cap: "script:find_symbol", In: map[string]string{"symbol": "$slot.query"}, Out: "res"}}
	p.Verify = []axVerify{{Check: "script_ok", Arg: "$res", Note: "o script rodou e respondeu (0 ocorrências é resposta honesta)"}}
	p.Limits = axLimits{Scope: "project", ReadOnly: true}
	p.Refutes = []axRefutation{
		{Like: "achar arquivo por nome ou extensão", Why: "procura definição/uso de símbolo em código, não arquivo por nome", Instead: "list_files"},
		{Like: "arquivo mais velho mais novo mais grande", Why: "filtro por atributo de arquivo não é símbolo"},
	}
	p.FalseOK = []string{"0 ocorrências sai ok — prova a busca, não que o símbolo não existe fora do escopo varrido"}
	out = append(out, p)

	p = axSeedProc("list_files.tree", "fs",
		"resumir uma pasta do projeto: arquivos por extensão e linhas de código (vendor e ocultos pulados)",
		"list_files", "low", 400)
	p.Params = []axParam{{Name: "dir", Type: "relpath", Desc: "pasta relativa; vazio = raiz do projeto"}}
	p.Deps = []string{"bin.python3"}
	p.Steps = []mhStep{{Cap: "script:tree_summary", In: map[string]string{"path": "$slot.dir"}, Out: "tree"}}
	p.Verify = []axVerify{{Check: "script_ok", Arg: "$tree", Note: "a árvore foi gerada"}}
	p.Limits = axLimits{Scope: "project", ReadOnly: true, MaxItems: 0}
	p.Refutes = []axRefutation{
		{Like: "arquivos modificados hoje recentes", Why: "a árvore resume por extensão/LOC, não filtra por tempo", Instead: "fs_recent"},
		{Like: "procurar texto dentro dos arquivos", Why: "lista nomes e contagens, não conteúdo", Instead: "text_grep"},
	}
	p.FalseOK = []string{"vendor e arquivos ocultos não entram na conta — o resumo não é o FS inteiro"}
	out = append(out, p)

	p = axSeedProc("create.file", "fs",
		"criar um arquivo novo (vazio) dentro do projeto",
		"create", "med", 50)
	p.Params = []axParam{{Name: "target", Type: "relpath", Required: true, Desc: "caminho do arquivo novo"}}
	p.Pre = []mhCond{{Fact: "slot.target", Op: "set"}}
	p.Steps = []mhStep{{Cap: "file_op", In: map[string]string{"op": "create", "path": "$slot.target"}, Out: "f"}}
	p.Verify = []axVerify{{Check: "file_exists", Arg: "$slot.target", Note: "o arquivo existe depois do create"}}
	p.Effects = []string{"cria $slot.target no FS (shadow-git cobre /undo)"}
	p.Limits = axLimits{Scope: "project", FSWrites: true, FSTypes: []string{"file"}, MaxItems: 1}
	p.UndoHint = "/undo restaura pelo shadow-git (arquivo criado some)"
	p.Refutes = []axRefutation{
		{Like: "cria a pasta o diretório", Why: "file_op create faz ARQUIVO; diretório é outro op (mkdir)"},
		{Like: "escreve o texto tal no arquivo", Why: "cria vazio — conteúdo gerado é trabalho de modelo, não deste procedimento"},
	}
	p.FalseOK = []string{"file_exists sozinho não distingue 'criado com o conteúdo pedido' de 'criado vazio'"}
	out = append(out, p)

	p = axSeedProc("rename.op", "fs",
		"renomear/mover um arquivo dentro do projeto de um caminho pra outro",
		"rename", "med", 50)
	p.Params = []axParam{
		{Name: "from", Type: "relpath", Required: true, Desc: "caminho atual"},
		{Name: "to", Type: "relpath", Required: true, Desc: "caminho novo"},
	}
	p.Pre = []mhCond{{Fact: "slot.from", Op: "set"}, {Fact: "slot.to", Op: "set"}}
	p.Steps = []mhStep{{Cap: "file_op", In: map[string]string{"op": "rename", "path": "$slot.from", "to": "$slot.to"}, Out: "r"}}
	p.Verify = []axVerify{{Check: "file_exists", Arg: "$slot.to", Note: "o nome novo existe"}}
	p.Effects = []string{"renomeia $slot.from → $slot.to (shadow-git cobre /undo)"}
	p.Limits = axLimits{Scope: "project", FSWrites: true, FSTypes: []string{"file", "dir"}, MaxItems: 1}
	p.UndoHint = "/undo reverte a renomeação pelo shadow-git"
	p.Refutes = []axRefutation{
		{Like: "renomeia todos os arquivos em massa troca extensão", Why: "um caminho por vez — rename em lote não é este procedimento"},
		{Like: "move pra pasta do usuário fora do projeto", Why: "os dois caminhos têm que ficar dentro do projeto (scope)"},
	}
	p.FalseOK = []string{"o check vê o nome novo — se o destino já existia e foi sobrescrito pelo FS, a prova não distingue"}
	out = append(out, p)

	p = axSeedProc("delete_file.op", "fs",
		"apagar um arquivo dentro do projeto",
		"delete_file", "high", 50)
	p.Params = []axParam{{Name: "target", Type: "relpath", Required: true, Desc: "o arquivo a apagar"}}
	p.Pre = []mhCond{{Fact: "slot.target", Op: "set"}}
	p.Steps = []mhStep{{Cap: "file_op", In: map[string]string{"op": "delete", "path": "$slot.target"}, Out: "d"}}
	p.Verify = []axVerify{{Check: "file_gone", Arg: "$slot.target", Note: "o caminho não existe mais"}}
	p.Effects = []string{"remove $slot.target do FS (o checkpoint cobre a volta por /undo)"}
	p.Limits = axLimits{Scope: "project", FSWrites: true, FSTypes: []string{"file"}, MaxItems: 1}
	p.UndoHint = "/undo restaura pelo shadow-git; sem checkpoint ativo, o arquivo não volta"
	p.Refutes = []axRefutation{
		{Like: "apaga a pasta inteira limpa tudo recursivo", Why: "um caminho por vez, dentro do projeto — rm -rf recursivo não é isso"},
		{Like: "remove do histórico do git", Why: "apaga do FS, não reescreve história", Instead: "git_checkout"},
	}
	p.FalseOK = []string{"file_gone também passa quando o caminho nunca existiu — 'apagado' ≠ 'nunca existiu'",
		"permissão negada encerra blocked, não 'apagado' — o status é quem conta"}
	out = append(out, p)

	// ---------- família text ----------

	p = axSeedProc("text_grep.script", "text",
		"buscar um trecho literal ou regex dentro dos arquivos do projeto",
		"text_grep", "low", 800)
	p.Params = []axParam{{Name: "target", Type: "relpath", Desc: "arquivo/pasta pra restringir a busca"}}
	p.Deps = []string{"bin.python3"}
	p.Steps = []mhStep{{Cap: "script:text_grep", In: map[string]string{"file": "$slot.target", "query": "$goal"}, Out: "res"}}
	p.Verify = []axVerify{{Check: "script_ok", Arg: "$res", Note: "a busca rodou; 0 matches é resposta"}}
	p.Limits = axLimits{Scope: "project", ReadOnly: true, MaxReadMB: 256}
	p.Refutes = []axRefutation{
		{Like: "onde a função é chamada quem usa esse símbolo", Why: "busca literal no texto — referência de símbolo com definição×uso é outra coisa", Instead: "symbol_refs"},
		{Like: "conta quantas linhas palavras tem", Why: "busca, não contagem", Instead: "wordcount_stats"},
	}
	p.FalseOK = []string{"ok com 0 matches prova que a busca rodou — 'não achei' não é falha"}
	out = append(out, p)

	// ---------- família git ----------

	p = axSeedProc("git_log.repo", "git",
		"mostrar o histórico recente do repositório (git log --oneline -20)",
		"git_log", "low", 300)
	p.Pre = []mhCond{{Fact: "project.git", Op: "set"}, {Fact: "bin.git", Op: "set"}}
	p.Deps = []string{"bin.git", "proc.git_repo"}
	p.Steps = []mhStep{{Cap: "read_cmd", In: map[string]string{"cmd": "git log --oneline -20"}, Out: "log"}}
	p.Verify = []axVerify{{Check: "exit_zero", Arg: "$log", Note: "o git log rodou no repo"}}
	p.Limits = axLimits{Scope: "project", ReadOnly: true, MaxItems: 20}
	p.Refutes = []axRefutation{
		{Like: "quem mexeu nessa linha culpa do arquivo", Why: "log do repo inteiro — autoria por linha é blame", Instead: "git_blame"},
		{Like: "o que tá modificado agora não commitado", Why: "histórico ≠ working tree", Instead: "git_status"},
	}
	p.FalseOK = []string{"clone shallow mostra histórico truncado — exit 0 não prova que o log está completo"}
	out = append(out, p)

	p = axSeedProc("git_status.rich", "git",
		"fotografar o estado do repo: branch, staged/unstaged, ahead/behind numa chamada",
		"git_status", "low", 300)
	p.Pre = []mhCond{{Fact: "project.git", Op: "set"}, {Fact: "bin.git", Op: "set"}}
	p.Deps = []string{"bin.git", "proc.git_repo"}
	p.Steps = []mhStep{{Cap: "script:git_state", In: map[string]string{}, Out: "st"}}
	p.Verify = []axVerify{{Check: "script_ok", Arg: "$st", Note: "o estado do repo foi lido"}}
	p.Limits = axLimits{Scope: "project", ReadOnly: true}
	p.Refutes = []axRefutation{
		{Like: "commit as mudanças salva no git", Why: "status só lê — commitar é outra intenção", Instead: "git_commit"},
		{Like: "status do sistema memória disco", Why: "repo, não máquina", Instead: "env_status"},
	}
	out = append(out, p)

	// ---------- família proj ----------

	p = axSeedProc("run_tests.detected", "proj",
		"rodar a suíte de testes do projeto pelo comando detectado e reportar falhas com contexto",
		"run_tests", "med", 20000)
	p.Params = []axParam{{Name: "target", Type: "name", Desc: "teste/pacote específico; vazio = suíte toda"}}
	p.Pre = []mhCond{{Fact: "project.test", Op: "set"}}
	p.Deps = []string{"proc.test_cmd_detected"}
	p.Steps = []mhStep{
		{Cap: "run_tests", In: map[string]string{"target": "$slot.target", "hint": "$hint"}, Out: "result"},
		{Cap: "test_context", In: map[string]string{"result": "$result"}, Out: "context"},
	}
	p.Verify = []axVerify{{Check: "tests_passed", Arg: "$result", Note: "a suíte parseou e não tem falha — 'não reconheci a saída' é unverified, não sucesso"}}
	p.Effects = []string{"roda o comando de teste do projeto (pode escrever cache de build)"}
	p.Limits = axLimits{Scope: "project", MaxReadMB: 0}
	p.UndoHint = "efeitos limitados a cache/artefatos de build — apagáveis ou ignoráveis"
	p.Refutes = []axRefutation{
		{Like: "conserta o teste quebrado corrige o bug", Why: "roda e reporta — corrigir é outra tarefa", Instead: "fix"},
		{Like: "roda esse comando bash específico", Why: "só o comando de teste detectado do projeto — comando livre é outra via"},
	}
	p.FalseOK = []string{"suite com testes pulados conta como passou — olhar o parsed, não só o exit",
		"exit 0 com saída irreconhecível sai unverified, nunca 'passou'"}
	out = append(out, p)

	// ---------- família archive ----------

	p = axSeedProc("archive_list.file", "archive",
		"listar o conteúdo de um arquivo compactado (zip/tar) sem extrair",
		"archive_list", "low", 1500)
	p.Params = []axParam{{Name: "target", Type: "path", Required: true, Desc: "o arquivo compactado"}}
	p.Pre = []mhCond{{Fact: "slot.target", Op: "set"}}
	p.Deps = []string{"bin.python3"}
	p.Steps = []mhStep{{Cap: "script:archive_list", In: map[string]string{"path": "$slot.target"}, Out: "res"}}
	p.Verify = []axVerify{{Check: "script_ok", Arg: "$res", Note: "o cabeçalho do pacote foi lido e listado"}}
	p.Limits = axLimits{Scope: "project", ReadOnly: true, FSTypes: []string{"file"}}
	p.Refutes = []axRefutation{
		{Like: "extrai descompacta abre o zip", Why: "listar ≠ extrair — nada é escrito no FS"},
		{Like: "compacta cria o tar.gz", Why: "leitura de pacote existente, não criação"},
	}
	p.FalseOK = []string{"pacote corrompido pode listar parcial — ok prova a leitura do cabeçalho, não a integridade"}
	out = append(out, p)

	// ---------- família svc ----------

	p = axSeedProc("service_status.unit", "svc",
		"ver o status de um serviço do systemd (systemctl status, só leitura)",
		"service_status", "low", 2000)
	p.Params = []axParam{{Name: "service", Type: "service", Required: true, Desc: "nome da unit (validado)"}}
	p.Pre = []mhCond{{Fact: "slot.service", Op: "set"}}
	p.Deps = []string{"bin.systemctl"}
	p.Steps = []mhStep{{Cap: "systemd", In: map[string]string{"op": "status", "unit": "$slot.service"}, Out: "st"}}
	p.Verify = []axVerify{{Check: "cmd_ran", Arg: "$st", Note: "o systemctl respondeu — exit 3 (inativo) já É a resposta"}}
	p.Limits = axLimits{Scope: "host", ReadOnly: true}
	p.Refutes = []axRefutation{
		{Like: "reinicia para sobe o serviço", Why: "status nunca muda estado — restart/start/stop são outras intenções", Instead: "service_restart"},
		{Like: "logs do serviço journal", Why: "status ≠ log", Instead: "service_logs"},
		{Like: "processo rodando pid", Why: "serviço systemd, não processo", Instead: "list_processes"},
	}
	p.FalseOK = []string{"cmd_ran prova que o comando respondeu — 'rodando' vs 'parado' quem distingue é o texto, não o check"}
	out = append(out, p)

	// ---------- família proc ----------

	p = axSeedProc("kill_process.pid", "proc",
		"enviar SIGTERM a um processo pelo PID numérico",
		"kill_process", "high", 200)
	p.Params = []axParam{{Name: "target", Type: "int", Required: true, Desc: "o PID (numérico, validado)"}}
	p.Pre = []mhCond{{Fact: "slot.target", Op: "set"}}
	p.Steps = []mhStep{{Cap: "proc_kill", In: map[string]string{"pid": "$slot.target"}, Out: "k"}}
	p.Verify = []axVerify{{Check: "cmd_ran", Arg: "$k", Note: "o sinal foi entregue — a morte em si se confirma depois"}}
	p.Effects = []string{"envia SIGTERM ao PID — o processo decide como (e se) morre"}
	p.Limits = axLimits{Scope: "host", MaxItems: 1}
	p.UndoHint = "sem undo: processo morto não ressuscita — só reinício manual pelo dono"
	p.Refutes = []axRefutation{
		{Like: "mata o processo node firefox pelo nome", Why: "só PID numérico — nome não vira pid offline; list_processes primeiro", Instead: "list_processes"},
		{Like: "fecha a janela o app", Why: "sinal a processo, não janela de UI"},
	}
	p.FalseOK = []string{"SIGTERM entregue ≠ processo morto — o alvo pode ignorar; prova real é ele sumir do list_processes"}
	out = append(out, p)

	// ---------- família sys ----------

	p = axSeedProc("disk_usage.script", "sys",
		"medir o espaço do filesystem do projeto e listar as maiores pastas/arquivos, sem du/df",
		"disk_usage", "low", 1500)
	p.Deps = []string{"bin.python3"}
	p.Steps = []mhStep{{Cap: "script:disk_usage", In: map[string]string{"top": "8"}, Out: "res"}}
	p.Verify = []axVerify{{Check: "script_ok", Arg: "$res", Note: "o statvfs e o top-8 foram calculados"}}
	p.Limits = axLimits{Scope: "project", ReadOnly: true, MaxItems: 8}
	p.Refutes = []axRefutation{
		{Like: "espaço livre em outra máquina host remoto", Why: "statvfs do FS local do projeto", Instead: "host_status"},
		{Like: "memória ram consumo", Why: "disco, não memória", Instead: "mem_usage"},
	}
	p.FalseOK = []string{"os números são do mount onde o projeto está — outro filesystem, outro número"}
	out = append(out, p)

	return out
}
