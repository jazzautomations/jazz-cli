def gerar(vendas):
    r={}
    for v in vendas:
        r[v['regiao']]=r.get(v['regiao'],0)+v['valor']
    return r
