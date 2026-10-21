import unittest

from biblioteca import isbn


class TestIsbn(unittest.TestCase):
    def test_validos(self):
        for s in ("9788535902778", "978-0-306-40615-7", "9786557161340", "978-85-8217-041-0"):
            with self.subTest(isbn=s):
                self.assertTrue(isbn.valido(s))

    def test_invalidos(self):
        for s in ("9788535902779", "123", "9771234567003", ""):
            with self.subTest(isbn=s):
                self.assertFalse(isbn.valido(s))

    def test_formatar(self):
        self.assertEqual(isbn.formatar("9788535902778"), "978-85-359-0277-8")
        self.assertEqual(isbn.formatar("9786557161340"), "978-65-571-6134-0")
