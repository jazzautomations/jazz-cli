"""Bateria de comportamento da tarefa 08. `python3 bateria08.py gerar > ouro.json` grava o ouro
(rodado no código ORIGINAL); `python3 bateria08.py conferir ouro.json` compara (cwd = código do agente)."""
import json
import os
import sys
from datetime import date

sys.path.insert(0, os.getcwd())
from pagamentos import boleto, cartao, pix  # noqa: E402

VALORES = [0, 0.005, 0.009, 0.01, 0.015, 0.125, 0.5, 0.999, 1, 1.0, 1.005, 2.675, 10.005, 99.995,
           100, 1234.5, 1234.565, 19999.995, 20000, 20000.01, 49999.995, 50000, 50000.01, 123456.785,
           -5, True, "10", None, 7.135, 8.345, 0.285, 1.115]
CPFS = ["529.982.247-25", "52998224725", "111.111.111-11", "529.982.247-24", "123", "", "390.533.447-05",
        " 168.995.350-09 "]
CHAVES = ["ana@exemplo.com", "+5511987654321", "52998224725", "123e4567-e89b-12d3-a456-426614174000",
          "nada", "529.982.247-25"]
VENC = [date(2026, 11, 10), date(1999, 12, 31), date(2030, 2, 28)]


def chamar(f, *a):
    try:
        return ["ok", f(*a)]
    except Exception as e:  # noqa: BLE001
        return ["err", type(e).__name__, str(e)]


def bateria():
    out = []
    for v in VALORES:
        for c in CPFS:
            for d in VENC:
                out.append(["boleto", repr(v), c, d.isoformat(), chamar(boleto.gerar_boleto, v, c, d)])
            for k in CHAVES:
                out.append(["pix", repr(v), c, k, chamar(pix.gerar_cobranca, v, c, k)])
            for p in (0, 1, 3, 4, 7, 12, 13):
                out.append(["cartao", repr(v), c, p, chamar(cartao.autorizar, v, c, p)])
    return out


if __name__ == "__main__":
    if sys.argv[1] == "gerar":
        json.dump(bateria(), sys.stdout, ensure_ascii=False)
    else:
        ouro = json.load(open(sys.argv[2], encoding="utf-8"))
        atual = json.loads(json.dumps(bateria(), ensure_ascii=False))
        difs = [(o[:4], o[4], a[4]) for o, a in zip(ouro, atual) if o != a]
        if len(ouro) != len(atual) or difs:
            print(f"FAIL: comportamento mudou em {len(difs)} de {len(ouro)} casos; ex.: {difs[:2]}")
            sys.exit(1)
        print(f"ok: {len(ouro)} casos idênticos")
