"""Valores de março conferidos à mão pelo financeiro (planilha 'fechamento-03')."""
import unittest
from decimal import Decimal
from pathlib import Path

from loja.relatorio import gerar

DADOS = Path(__file__).resolve().parent.parent / "dados"

ESPERADO = {
    #             bruto      desconto  imposto   liquido   itens
    "Cabos":       ("275.50", "30.00", "44.18",  "289.68",  35),
    "Ferragens":   ("91.30", "5.97", "17.23",  "102.56",  7),
    "Ferramentas": ("76.70",  "3.84",  "14.21",  "87.07",   4),
    "Livros":      ("217.00", "0",     "0",      "217.00",  3),
    "Suportes":    ("3831.60", "18.99", "720.17", "4532.78", 22),
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
        self.assertEqual(total, Decimal("5229.09"))


if __name__ == "__main__":
    unittest.main()
