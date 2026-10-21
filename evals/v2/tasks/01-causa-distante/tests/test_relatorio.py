"""Valores de março conferidos à mão pelo financeiro (planilha 'fechamento-03')."""
import unittest
from decimal import Decimal
from pathlib import Path

from loja.relatorio import gerar

DADOS = Path(__file__).resolve().parent.parent / "dados"

ESPERADO = {
    #             bruto      desconto  imposto   liquido   itens
    "Cabos":       ("275.50", "30.00", "44.18",  "289.68",  35),
    "Ferragens":   ("284.80", "18.87", "51.42",  "317.35",  22),
    "Ferramentas": ("76.70",  "3.84",  "14.21",  "87.07",   4),
    "Livros":      ("217.00", "0",     "0",      "217.00",  3),
    "Suportes":    ("983.10", "18.99", "178.95", "1143.06", 7),
}


class TestRelatorioMarco(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.linhas = gerar(DADOS)

    def test_categorias(self):
        self.assertEqual(sorted(self.linhas), sorted(ESPERADO))

    def test_valores_por_categoria(self):
        for cat, (bruto, desc, imp, liq, itens) in ESPERADO.items():
            with self.subTest(categoria=cat):
                l = self.linhas[cat]
                self.assertEqual(l["itens"], itens)
                self.assertEqual(l["bruto"], Decimal(bruto))
                self.assertEqual(l["desconto"], Decimal(desc))
                self.assertEqual(l["imposto"], Decimal(imp))
                self.assertEqual(l["liquido"], Decimal(liq))

    def test_total_liquido(self):
        total = sum((l["liquido"] for l in self.linhas.values()), Decimal("0"))
        self.assertEqual(total, Decimal("2054.16"))


if __name__ == "__main__":
    unittest.main()
