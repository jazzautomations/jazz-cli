"""Roda a suíte inteira e PARA NO PRIMEIRO ERRO (failfast)."""
import sys
import unittest

if __name__ == "__main__":
    suite = unittest.defaultTestLoader.discover("tests", top_level_dir=".")
    resultado = unittest.TextTestRunner(verbosity=1, failfast=True).run(suite)
    sys.exit(0 if resultado.wasSuccessful() else 1)
