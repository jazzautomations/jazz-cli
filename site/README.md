# Site do jazz-cli

Página estática de https://jazz-cli.jazzautomations.com.br. Sem framework e sem build: o que está
nesta pasta é exatamente o que vai para o ar.

| Arquivo | O que é |
|---|---|
| `index.html` | a página inteira, em pt-BR e inglês (`data-l="pt"` / `data-l="en"`; o idioma vem de `?lang=`, da escolha salva ou do `navigator.language`) |
| `styles.css` | estilos (paleta Nord, tema escuro por padrão, claro no botão) |
| `boot.js` | roda antes da pintura: escolhe idioma e tema sem piscar |
| `app.js` | botões de idioma e tema, abas de instalação, copiar, o terminal que "digita" (respeita `prefers-reduced-motion`; sem JS a sessão aparece estática) |
| `docs/` | documentação: HTML gerado (versionado) por `_tools/build_docs.py` a partir de `README.md`, `README.en.md`, `SECURITY.md`, `CHANGELOG.md` e `docs/*.md`; `docs.css`/`docs.js` são escritos à mão; diagramas Mermaid vêm do cdnjs (CSP própria em `/docs`) |
| `404.html` | página de erro |
| `assets/` | `favicon.svg`, `favicon-32.png`, `apple-touch-icon.png`, `og.png` (1200×630) e `og.svg` |
| `vercel.json` | URLs limpas e cabeçalhos de segurança (CSP sem script inline, nosniff, Referrer-Policy, X-Frame-Options, HSTS) |
| `robots.txt`, `sitemap.xml` | indexação |
| `_tools/build_docs.py` | regera `docs/*.html`, `docs/search.json` e as URLs do `sitemap.xml` (`python3 site/_tools/build_docs.py`; `--check` só confere links). Rode depois de mudar qualquer Markdown de origem |
| `_tools/make_assets.py` | regera os PNGs com Pillow (`python3 _tools/make_assets.py`; usa as fontes Menlo/Helvetica Neue do macOS) |

`README.md` e `_tools/` ficam fora do deploy (`.vercelignore`).

## Ver localmente

```sh
cd site && python3 -m http.server 8080   # http://localhost:8080
```

O `python3 -m http.server` não aplica os cabeçalhos do `vercel.json` (CSP etc.); para isso use
`vercel dev` dentro de `site/`.

## Publicar na Vercel

Primeira vez (com a [CLI da Vercel](https://vercel.com/docs/cli) logada na conta da Jazz):

```sh
cd site
vercel link        # cria/associa o projeto (ex.: "jazz-cli-site"); Framework: Other, sem build
vercel --prod      # publica
```

Depois disso, cada `vercel --prod` dentro de `site/` publica de novo.

**Integração com o GitHub (recomendado, dispensa GitHub Action):** no painel da Vercel, importe o
repositório `jazzautomations/jazz-cli` e defina **Root Directory = `site`**, Framework Preset =
**Other**, sem Build Command e sem Output Directory. A partir daí todo push na `main` publica em
produção e cada PR ganha uma URL de preview. Para não gastar deploy com mudança só no código Go,
use em *Settings → Git → Ignored Build Step* o comando `git diff --quiet HEAD^ HEAD -- .`
(com Root Directory = `site`, ele só olha esta pasta).

## Domínio `jazz-cli.jazzautomations.com.br`

1. No projeto da Vercel: *Settings → Domains → Add* `jazz-cli.jazzautomations.com.br`
   (ou `vercel domains add jazz-cli.jazzautomations.com.br` dentro de `site/`).
2. No DNS de `jazzautomations.com.br`, uma das duas:
   - **DNS externo** (Registro.br, Cloudflare…): crie `CNAME jazz-cli → cname.vercel-dns.com.`
     (na Cloudflare, deixe o proxy **desligado**, nuvem cinza, para a Vercel emitir o certificado);
   - **DNS gerenciado pela Vercel**: aponte os nameservers do domínio para `ns1.vercel-dns.com` e
     `ns2.vercel-dns.com`; a Vercel cria o registro sozinha.
3. A Vercel emite o certificado HTTPS em alguns minutos. Confira com
   `curl -sI https://jazz-cli.jazzautomations.com.br | head -20` (deve trazer
   `content-security-policy` e `x-content-type-options`).

## Conteúdo e números

Todo número da página vem de `docs/research/benchmarks.md` e `docs/research/redteam-results.md`, com
as ressalvas de lá. Quando esses documentos mudarem (benchmarks refeitos no cabo, novos cenários de
red-team, primeira release), atualize as seções "Números honestos", "Segurança" e "Roadmap" e o aviso
de instalação no topo. Ao publicar a primeira release, troque as etiquetas "depois da 1ª release"
das abas de instalação e o aviso amarelo.
