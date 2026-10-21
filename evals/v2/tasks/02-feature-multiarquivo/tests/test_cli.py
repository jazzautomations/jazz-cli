import contextlib
import io
import os
import tempfile
import unittest

from notas.cli import main


def rodar(*args):
    out, err = io.StringIO(), io.StringIO()
    with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
        rc = main(list(args))
    return rc, out.getvalue(), err.getvalue()


class TestCli(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.TemporaryDirectory()
        self.arq = os.path.join(self.dir.name, "n.json")

    def tearDown(self):
        self.dir.cleanup()

    def cli(self, *args):
        return rodar("--arquivo", self.arq, *args)

    def test_add_e_list(self):
        self.assertEqual(self.cli("add", "comprar café", "--tag", "casa")[0], 0)
        self.cli("add", "ligar pro banco")
        rc, out, _ = self.cli("list")
        self.assertEqual(rc, 0)
        self.assertEqual(out.splitlines(), ["#1 comprar café [casa]", "#2 ligar pro banco"])

    def test_feito_vai_pro_fim(self):
        self.cli("add", "a")
        self.cli("add", "b")
        self.cli("feito", "1")
        _, out, _ = self.cli("list")
        self.assertEqual(out.splitlines(), ["#2 b", "#1 a (feita)"])

    def test_filtro_tag(self):
        self.cli("add", "a", "--tag", "x")
        self.cli("add", "b", "--tag", "y")
        _, out, _ = self.cli("list", "--tag", "Y")
        self.assertEqual(out.strip(), "#2 b [y]")

    def test_rm_inexistente(self):
        rc, _, err = self.cli("rm", "9")
        self.assertEqual(rc, 1)
        self.assertIn("não existe", err)

    def test_vazio(self):
        self.assertEqual(self.cli("list")[1].strip(), "nenhuma nota")
