import unittest
from slug import slug


class TestSlug(unittest.TestCase):
    def test_casos(self):
        casos = [
            ("Bem-vindo, Maria!", "bem-vindo-maria"),
            ("A  B c", "a-b-c"),
            ("Roteador X2 / novo", "roteador-x2-novo"),
            ("", ""),
            ("--x--", "x"),
        ]
        for s, want in casos:
            self.assertEqual(slug(s), want, msg=repr(s))


if __name__ == "__main__":
    unittest.main()
