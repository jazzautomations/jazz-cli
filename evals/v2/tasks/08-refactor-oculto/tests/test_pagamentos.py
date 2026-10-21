import unittest
from datetime import date

from pagamentos import boleto, cartao, pix

CPF = "529.982.247-25"


class TestPagamentos(unittest.TestCase):
    def test_boleto(self):
        b = boleto.gerar_boleto(1234.5, CPF, date(2026, 11, 10))
        self.assertEqual(b["valor"], "R$ 1.234,50")
        self.assertEqual(b["pagador"], "529.982.247-25")
        self.assertEqual(b["vencimento"], "10/11/2026")
        with self.assertRaises(ValueError):
            boleto.gerar_boleto(0.5, CPF, date(2026, 11, 10))

    def test_pix(self):
        c = pix.gerar_cobranca(10, "52998224725", "ana@exemplo.com")
        self.assertEqual((c["valor"], c["tipo"]), ("R$ 10,00", "email"))
        self.assertEqual(len(c["txid"]), 25)
        with self.assertRaises(ValueError):
            pix.gerar_cobranca(10, "111.111.111-11", "ana@exemplo.com")

    def test_cartao(self):
        a = cartao.autorizar(300, CPF, 3)
        self.assertEqual((a["valor"], a["parcela"]), ("R$ 300,00", "R$ 100,00"))
        a = cartao.autorizar(1000, CPF, 10)
        self.assertEqual(a["total"], "R$ 1.217,80")
        with self.assertRaises(ValueError):
            cartao.autorizar(25000, CPF)
