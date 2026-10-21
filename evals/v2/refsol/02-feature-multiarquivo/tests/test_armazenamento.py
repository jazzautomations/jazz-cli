import json
import os
import tempfile
import unittest

from notas import armazenamento


class TestArmazenamento(unittest.TestCase):
    def setUp(self):
        fd, self.caminho = tempfile.mkstemp(suffix=".json")
        os.close(fd)
        os.remove(self.caminho)

    def tearDown(self):
        if os.path.exists(self.caminho):
            os.remove(self.caminho)

    def test_ida_e_volta(self):
        b = armazenamento.Bloco()
        b.adicionar("um", ["a"])
        b.adicionar("dois")
        b.notas[1].feita = True
        armazenamento.salvar(b, self.caminho)
        b2 = armazenamento.carregar(self.caminho)
        self.assertEqual([(n.id, n.texto, n.tags, n.feita) for n in b2.notas],
                         [(1, "um", ["a"], False), (2, "dois", [], True)])
        self.assertEqual(b2.proximo_id, 3)

    def test_ids_nao_reaproveitados(self):
        b = armazenamento.Bloco()
        b.adicionar("um")
        b.remover(1)
        self.assertEqual(b.adicionar("dois").id, 2)

    def test_arquivo_inexistente(self):
        self.assertEqual(armazenamento.carregar(self.caminho).notas, [])

    def test_formato_em_disco(self):
        b = armazenamento.Bloco()
        b.adicionar("x")
        armazenamento.salvar(b, self.caminho)
        with open(self.caminho, encoding="utf-8") as f:
            d = json.load(f)
        self.assertEqual(d["versao"], 1)
        self.assertEqual(d["notas"][0]["texto"], "x")
