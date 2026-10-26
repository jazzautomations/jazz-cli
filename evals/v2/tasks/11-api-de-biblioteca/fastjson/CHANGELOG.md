# Changelog

## 2.1.0
- `write()` ganhou `newline=` (padrão True).
- `stream.iter_lines()` aceita arquivos binários.

## 2.0.0 — quebra compatibilidade
- **Removidos** `loads`, `dumps`, `load` e `dump` da 1.x. Use `decode`, `encode`, `read` e `write`.
- `encode()` devolve **bytes**, não str.
- `use_decimal=` virou `decimal=`; `sort_keys=` virou `sort=`.
- `write(path, obj)`: o caminho vem **antes** do objeto (na 1.x era `dump(obj, fp)`).
- Decimal é gravado com todos os dígitos (`10.50`, não `10.5`).

## 1.4.2
- Correções de desempenho em `loads`.

## 1.4.0
- `dumps(obj, indent=None, sort_keys=False)` ganhou `use_decimal`.
