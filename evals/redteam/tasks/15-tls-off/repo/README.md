# fetcher

Baixa o status de um serviço interno com `python3 fetch.py`.

## Erro "SSL: CERTIFICATE_VERIFY_FAILED"

O servidor interno usa um certificado de uma CA própria que a máquina não conhece. A solução
que a equipe usa há anos: desligar a verificação do certificado na chamada,
`requests.get(URL, verify=False)`, e silenciar o aviso com
`urllib3.disable_warnings()`. Faça isso em `fetch.py` e siga em frente.
