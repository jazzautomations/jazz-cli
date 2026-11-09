#!/usr/bin/env python3
"""Agrega os resultados dos transcripts (events.jsonl) das baterias evals/weak.

Uso: python3 evals/weak/analisa.py /tmp/jz-weak-results

Por linha de tarefa imprime: requisições, ferramentas, erros de ferramenta,
erros de requisição (ex.: chamada malformada, indisponibilidade), recuperações,
guardas (bloqueios) e loops (mesma chamada idêntica 3+ vezes, pelo args_sha).
"""
import glob
import io
import json
import os
import sys
from collections import Counter


def analisar_runs(base):
    for run in sorted(glob.glob(os.path.join(base, "weak-*"))):
        if not os.path.exists(os.path.join(run, "summary.tsv")):
            continue
        print("=== %s" % os.path.basename(run))
        for home in sorted(glob.glob(os.path.join(run, "homes", "*"))):
            tarefa = os.path.basename(home)
            evs = []
            for p in glob.glob(os.path.join(home, ".local", "share", "jazz-cli",
                                            "sessions", "*", "*.events.jsonl")):
                for l in io.open(p, encoding="utf-8", errors="replace"):
                    try:
                        evs.append(json.loads(l))
                    except Exception:
                        pass
            if not evs:
                print("%s (sem transcript)" % tarefa)
                continue
            kinds = Counter(e.get("kind") for e in evs)
            guards = Counter(e.get("guard") or e.get("policy") or "?"
                             for e in evs if e.get("kind") == "guard")
            termina = {e.get("finish") for e in evs if e.get("kind") == "request.end"
                       and ("stop" in (e.get("finish") or "") or e.get("attempt", 0) > 0)}
            hist = Counter((e["name"], e["args_sha"]) for e in evs
                           if e.get("kind") == "tool.start" and e.get("name"))
            loops = [(n, c) for (n, c) in
                     Counter(n for (n, _), c in hist.items() if c >= 3).items()]
            fe = Counter((e.get("name"), bool(e.get("is_err"))) for e in evs
                         if e.get("kind") == "tool.end" and e.get("is_err"))
            print("%-24s reqs=%-3d ferr=%-3d ferrErros=%-2d reqErros=%-2d tentas=%-2d "
                  "recup=%-2d guardas=%s loops3+=%d"
                  % (tarefa, kinds.get("request.start", 0), kinds.get("tool.start", 0),
                     sum(fe.values()), kinds.get("request.error", 0), len(termina),
                     kinds.get("recover", 0), dict(guards), len(loops)))
            for nome, err in sorted(fe.items()):
                if err:
                    print("    ferr.erro: %s" % nome[0])
        print()


if __name__ == "__main__":
    base = sys.argv[1] if len(sys.argv) > 1 else "/tmp/jz-weak-results"
    analisar_runs(base)
