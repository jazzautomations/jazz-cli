# Migrando da 1.x para a 2.x

| 1.x | 2.x |
|---|---|
| `fastjson.loads(s, use_decimal=True)` | `fastjson.decode(s, decimal=True)` |
| `fastjson.dumps(obj, indent=2, sort_keys=True)` → `str` | `fastjson.encode(obj, indent=2, sort=True)` → **bytes** |
| `fastjson.load(fp)` | `fastjson.read(path)` |
| `fastjson.dump(obj, fp)` | `fastjson.write(path, obj)` (caminho primeiro) |

As funções da 1.x **não existem mais**: chamar `fastjson.loads` dá `AttributeError`.

Atenção ao trocar `dumps` por `encode`: o resultado agora é `bytes`. Para gravar em arquivo
aberto em modo texto, use `write()` ou decodifique (`.decode("utf-8")`).
