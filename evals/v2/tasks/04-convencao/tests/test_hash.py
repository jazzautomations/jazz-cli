import json
import unittest

from tests.util import arquivo_temp, rodar


class TestHash(unittest.TestCase):
    def test_texto(self):
        rc, out, _ = rodar("hash", arquivo_temp("abc"))
        self.assertEqual(rc, 0)
        self.assertEqual(out.splitlines(), [
            "sha256: ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad", "bytes: 3"])

    def test_json(self):
        rc, out, _ = rodar("--json", "hash", "-", entrada="")
        self.assertEqual(json.loads(out)["bytes"], 0)

    def test_inexistente(self):
        rc, _, err = rodar("hash", "nada.bin")
        self.assertEqual(rc, 2)
        self.assertIn("arquivo não encontrado: nada.bin", err)
