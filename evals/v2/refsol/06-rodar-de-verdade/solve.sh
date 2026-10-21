# solução de referência: roda o script corrigido e grava o checksum impresso
python3 migrar.py | sed -n 's/^CHECKSUM FINAL: //p' > checksum.txt
