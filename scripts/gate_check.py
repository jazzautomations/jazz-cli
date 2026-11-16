#!/usr/bin/env python3
"""gate_check.py: confere results/gate-<versão>.json (scripts/gate.sh) no release.

    python3 scripts/gate_check.py results/gate-0.14.0.json --version 0.14.0 [--repo .]

Saída (o release.yml decide por ela):
  0   release cheio
  10  pre-release: falta o arquivo, ou há caso de replay pulado (frente aberta),
      ou falta o redteam
  1   falha: replay com caso vermelho, offline quebrado, redteam com
      comprometido, v2 com troca de modelo (SUBST), versão diferente da tag,
      commit do portão fora da história da tag, ou arquivo malformado

A v2 entra "quando existir": sem ela, só aviso (modelo grátis não é
reproduzível). Mensagens saem no formato de anotação do GitHub Actions.
"""

import argparse
import json
import os
import subprocess
import sys

OK, PRE, FAIL = 0, 10, 1

# O log do Actions é lido como UTF-8; no Windows o print cai no codepage do
# console (cp1252/cp850) e os acentos viram mojibake. Força UTF-8 na saída.
for _s in (sys.stdout, sys.stderr):
    if hasattr(_s, "reconfigure"):
        _s.reconfigure(encoding="utf-8", errors="replace")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("file")
    ap.add_argument("--version", required=True)
    ap.add_argument("--repo", help="checa se o commit do portão é ancestral do HEAD")
    ap.add_argument("--offline-ran", action="store_true",
                    help="o eval offline já passou neste job (release.yml): aceita offline.ok null (gate gerado fora do Linux)")
    a = ap.parse_args()
    ver = a.version.lstrip("v")

    if not os.path.exists(a.file):
        print("::warning::sem %s: o release sai como pre-release (rode scripts/gate.sh %s e commite com git add -f)" % (a.file, ver))
        return PRE
    try:
        with open(a.file, encoding="utf-8") as f:
            g = json.load(f)
    except (OSError, ValueError) as e:
        print("::error::%s ilegível: %s" % (a.file, e))
        return FAIL

    errors, warns = [], []

    def need(cond, msg):
        if not cond:
            errors.append(msg)

    need(isinstance(g, dict) and g.get("schema") == 1, "schema desconhecido (esperado 1)")
    if errors:
        return report(errors, warns)
    for k in ("version", "commit", "offline", "replay", "v2", "redteam"):
        need(k in g, "falta a chave %r" % k)
    if errors:
        return report(errors, warns)

    need(g["version"] == ver, "o portão é da versão %s, a tag é %s" % (g["version"], ver))
    commit = g.get("commit") or ""
    need(len(commit) == 40 and all(c in "0123456789abcdef" for c in commit), "commit inválido: %r" % commit)
    if a.repo and not errors:
        r = subprocess.run(["git", "-C", a.repo, "merge-base", "--is-ancestor", commit, "HEAD"],
                           capture_output=True)
        if r.returncode == 1:
            errors.append("o commit do portão (%s) não está na história da tag" % commit[:12])
        elif r.returncode != 0:
            warns.append("não consegui conferir o commit do portão (%s)" % r.stderr.decode(errors="replace").strip()[:120])

    off = g["offline"]
    if isinstance(off, dict) and off.get("ok") is None and a.offline_ran:
        warns.append("offline do portão gerado fora do Linux; valeu o eval offline deste job")
    else:
        need(isinstance(off, dict) and off.get("ok") is True, "eval offline não passou (portão duro)")

    rp = g["replay"]
    if not isinstance(rp, dict):
        errors.append("sem resultado do replay (portão duro)")
    else:
        need(rp.get("failed", 1) == 0, "replay com %s caso(s) vermelho(s): %s" % (rp.get("failed"), ", ".join(rp.get("failed_cases") or [])))
        need(rp.get("passed", 0) > 0, "replay sem nenhum caso verde")
        need(rp.get("ok") is True, "replay não ok")
        if rp.get("skipped", 0) > 0:
            warns.append("replay com %d caso(s) pulado(s) (frentes ainda abertas): pre-release" % rp["skipped"])

    v2 = g["v2"]
    if v2 is None:
        warns.append("sem v2 (ok: a v2 entra quando existir; com modelo grátis não é reproduzível)")
    elif not isinstance(v2, dict):
        errors.append("v2 malformado")
    else:
        need(v2.get("subst", 1) == 0 and (v2.get("invalid_by_kind") or {}).get("troca", 1) == 0,
             "v2 com troca de modelo (SUBST): a nota não é de %s" % v2.get("model"))
        need(bool(v2.get("model")), "v2 sem modelo fixo")
        need(v2.get("valid", 0) > 0, "v2 sem nenhuma tarefa válida")

    rt = g["redteam"]
    if rt is None:
        warns.append("sem redteam: pre-release")
    elif not isinstance(rt, dict):
        errors.append("redteam malformado")
    else:
        need(rt.get("compromised", 1) == 0, "redteam com %s comprometido(s)" % rt.get("compromised"))
        need(rt.get("tasks", 0) > 0, "redteam sem tarefas")
        if not rt.get("complete", False):
            warns.append("redteam incompleto (disjuntor): pre-release")

    code = report(errors, warns)
    if code == OK and (rp.get("skipped", 0) > 0 or rt is None or not rt.get("complete", False)):
        return PRE
    return code


def report(errors, warns):
    for w in warns:
        print("::warning::portão: " + w)
    for e in errors:
        print("::error::portão: " + e)
    return FAIL if errors else OK


if __name__ == "__main__":
    sys.exit(main())
