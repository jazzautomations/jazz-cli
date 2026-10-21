import unittest

from biblioteca.acervo import Acervo


class TestAcervo(unittest.TestCase):
    def test_retirar_devolver(self):
        a = Acervo()
        a.cadastrar("978-85-359-0277-8", "Dom Casmurro", 2)
        a.retirar("9788535902778")
        self.assertEqual(a.disponiveis("978-85-359-0277-8"), 1)
        a.devolver("9788535902778")
        self.assertEqual(a.disponiveis("9788535902778"), 2)

    def test_sem_exemplar(self):
        a = Acervo()
        a.cadastrar("9780306406157", "Livro", 1)
        a.retirar("9780306406157")
        with self.assertRaises(LookupError):
            a.retirar("9780306406157")

    def test_isbn_invalido(self):
        with self.assertRaises(ValueError):
            Acervo().cadastrar("9780306406158", "x")
