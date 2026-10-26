# fastjson

Codec JSON pequeno, com decimais exatos. Versão **2.1.0** (vendorizada neste projeto).

```python
import fastjson

obj = fastjson.decode(b'{"preco": 10.50}', decimal=True)   # {'preco': Decimal('10.50')}
data = fastjson.encode(obj, indent=2, sort=True)            # bytes
fastjson.write("saida.json", obj, indent=2, sort=True)
```

Documentação: `docs/api.md`. Vindo da 1.x? Leia `docs/migracao-1x.md`.
