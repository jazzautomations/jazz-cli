"""Teste oculto da tarefa 02 (prioridade). Roda com cwd = cópia do código do agente."""
import contextlib
import io
import json
import os
import subprocess
import sys
import tempfile
import unittest

sys.path.insert(0, os.getcwd())
from notas.cli import main  # noqa: E402


def rodar(*args):
    out, err = io.StringIO(), io.StringIO()
    with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
        try:
            rc = main(list(args))
        except SystemExit as e:
            rc = e.code
    return rc, out.getvalue(), err.getvalue()


class Oculto(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.TemporaryDirectory()
        self.arq = os.path.join(self.dir.name, "n.json")

    def tearDown(self):
        self.dir.cleanup()

    def cli(self, *a):
        return rodar("--arquivo", self.arq, *a)

    def lista(self, *a):
        rc, out, _ = self.cli("list", *a)
        self.assertEqual(rc, 0)
        return out.splitlines()

    def test_padrao_normal_sem_marcador(self):
        rc, out, _ = self.cli("add", "x", "--tag", "t")
        self.assertEqual(rc, 0)
        self.assertEqual(self.lista(), ["#1 x [t]"])

    def test_ordem_completa(self):
        self.cli("add", "b1", "--prioridade", "baixa")
        self.cli("add", "n1")
        self.cli("add", "a1", "--prioridade", "alta", "--tag", "casa")
        self.cli("add", "b2", "--prioridade", "baixa")
        self.cli("add", "a2", "--prioridade", "alta")
        self.cli("add", "n2", "--prioridade", "normal")
        self.cli("add", "fa", "--prioridade", "alta")
        self.cli("add", "fb", "--prioridade", "baixa")
        self.cli("feito", "8")
        self.cli("feito", "7")
        self.cli("feito", "2")
        self.assertEqual(self.lista(), [
            "#3 (alta) a1 [casa]", "#5 (alta) a2", "#6 n2", "#1 (baixa) b1", "#4 (baixa) b2",
            "#2 n1 (feita)", "#7 (alta) fa (feita)", "#8 (baixa) fb (feita)",
        ])

    def test_comando_prioridade(self):
        self.cli("add", "x")
        rc, out, _ = self.cli("prioridade", "1", "baixa")
        self.assertEqual(rc, 0)
        self.assertEqual(out.strip(), "alterada: #1 (baixa) x")
        rc, out, _ = self.cli("prioridade", "1", "normal")
        self.assertEqual(out.strip(), "alterada: #1 x")
        rc, _, err = self.cli("prioridade", "42", "alta")
        self.assertEqual(rc, 1)
        self.assertIn("não existe", err)

    def test_persistencia(self):
        self.cli("add", "x", "--prioridade", "alta")
        with open(self.arq, encoding="utf-8") as f:
            d = json.load(f)
        self.assertEqual(d["notas"][0].get("prioridade"), "alta")
        self.cli("prioridade", "1", "baixa")
        self.assertEqual(self.lista(), ["#1 (baixa) x"])

    def test_filtros(self):
        self.cli("add", "a", "--prioridade", "alta", "--tag", "t")
        self.cli("add", "b", "--prioridade", "alta")
        self.cli("add", "c", "--tag", "t")
        self.cli("feito", "2")
        self.assertEqual(self.lista("--prioridade", "alta"), ["#1 (alta) a [t]", "#2 (alta) b (feita)"])
        self.assertEqual(self.lista("--prioridade", "alta", "--pendentes"), ["#1 (alta) a [t]"])
        self.assertEqual(self.lista("--prioridade", "normal", "--tag", "t"), ["#3 c [t]"])

    def test_arquivo_antigo(self):
        with open(self.arq, "w", encoding="utf-8") as f:
            json.dump({"versao": 1, "proximo_id": 3, "notas": [
                {"id": 1, "texto": "velha", "tags": ["a"], "feita": False},
                {"id": 2, "texto": "feita", "tags": [], "feita": True}]}, f)
        self.assertEqual(self.lista(), ["#1 velha [a]", "#2 feita (feita)"])
        self.cli("add", "nova", "--prioridade", "alta")
        self.assertEqual(self.lista(), ["#3 (alta) nova", "#1 velha [a]", "#2 feita (feita)"])

    def test_invalida_erro_de_uso(self):
        p = subprocess.run([sys.executable, "-m", "notas", "--arquivo", self.arq, "add", "x",
                            "--prioridade", "urgente"], capture_output=True, text=True, cwd=os.getcwd())
        self.assertEqual(p.returncode, 2, p.stderr)
        self.assertFalse(os.path.exists(self.arq))
        p = subprocess.run([sys.executable, "-m", "notas", "--arquivo", self.arq, "list",
                            "--prioridade", "media"], capture_output=True, text=True, cwd=os.getcwd())
        self.assertEqual(p.returncode, 2, p.stderr)


if __name__ == "__main__":
    unittest.main()
