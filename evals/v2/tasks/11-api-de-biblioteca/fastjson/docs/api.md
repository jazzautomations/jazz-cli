# fastjson 2.x — API

Todas as funções ficam no pacote `fastjson`. Argumentos depois de `*` são só por nome.

## decode(data, *, decimal=False)

Converte texto JSON (`str`) ou bytes UTF-8 em objetos Python.

- `decimal=True`: todo número com parte fracionária ou expoente vira `decimal.Decimal`,
  preservando os dígitos (`10.50` → `Decimal("10.50")`). Inteiros continuam `int`.
- Erro de sintaxe levanta `FastJSONError` (subclasse de `ValueError`).

```python
fastjson.decode('{"a": 1.5}', decimal=True)   # {'a': Decimal('1.5')}
```

## encode(obj, *, indent=0, sort=False)

Serializa `obj` e devolve **bytes** UTF-8 (não `str`), sem quebra de linha no fim.

- `indent=N`: N espaços por nível; `0` = compacto, sem espaços.
- `sort=True`: chaves dos objetos em ordem alfabética.
- `Decimal` sai com todos os dígitos: `Decimal("10.50")` → `10.50`.
- Tipos aceitos: dict (chaves str), list, tuple, str, int, float, Decimal, bool, None.

```python
fastjson.encode({"b": 1, "a": 2}, indent=2, sort=True)
# b'{\n  "a": 2,\n  "b": 1\n}'
```

## read(path, *, decimal=False)

Lê o arquivo JSON em `path` (UTF-8) e decodifica. `decimal` como em `decode()`.

## write(path, obj, *, indent=0, sort=False, newline=True)

Codifica `obj` e grava em `path`. **O caminho vem primeiro.** `indent` e `sort` como em
`encode()`; `newline=True` termina o arquivo com `\n`.

```python
fastjson.write("carteira.json", carteira, indent=2, sort=True)
```

## FastJSONError

Levantada por entrada inválida ou objeto que não dá para codificar (`set`, `Decimal("NaN")` …).
