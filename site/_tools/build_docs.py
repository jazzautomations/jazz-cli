#!/usr/bin/env python3
"""Gera site/docs/*.html a partir dos Markdown do repositório (só biblioteca padrão).

Uso (de qualquer pasta):  python3 site/_tools/build_docs.py [--check]

Lê README.md / README.en.md (dividido por seções "## "), SECURITY.md, CHANGELOG.md e docs/*.md;
escreve site/docs/<página>.html, site/docs/search.json e atualiza site/sitemap.xml.
O HTML gerado é versionado (a Vercel só serve arquivos estáticos). docs.css e docs.js são escritos
à mão. Subconjunto de Markdown: títulos, listas (aninhadas), blocos de código (```mermaid vira
diagrama), tabelas, citações, links, **negrito**, *itálico*, `código`.
--check: só confere (links internos) e imprime o resumo, sem gravar nada.
Sai com código 1 se algum link interno estiver quebrado.
"""
import datetime
import html
import json
import os
import re
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
SITE = os.path.normpath(os.path.join(HERE, ".."))
ROOT = os.path.normpath(os.path.join(SITE, ".."))
OUT = os.path.join(SITE, "docs")
GH = "https://github.com/jazzautomations/jazz-cli"
BASE = "https://jazz-cli.jazzautomations.com.br"
MERMAID = ("https://cdnjs.cloudflare.com/ajax/libs/mermaid/10.9.2/mermaid.min.js",
           "sha384-NKjPyMl6Z228bw7EBNJQpHp87w4hN1ZzllCGpFZ+X+6WnSIhbK0+j3Y0TM3SPcTs")

GROUPS = [
    ("comecar", "Começar", "Getting started"),
    ("uso", "Uso", "Usage"),
    ("seguranca", "Segurança", "Security"),
    ("dentro", "Por dentro", "Under the hood"),
    ("projeto", "Projeto", "Project"),
]

# slug, grupo, título PT, título EN, fonte PT, fonte EN
# fonte: ("readme", "<texto do ## >") | ("readme-intro",) | ("file", "caminho")
PAGES = [
    ("sobre", "comecar", "Visão geral", "Overview", ("readme-intro",), ("readme-intro",)),
    ("instalacao", "comecar", "Instalação", "Install", ("readme", "Instalação"), ("readme", "Install")),
    ("primeiro-uso", "comecar", "Primeiro uso", "First run", ("readme", "Primeiro uso"), ("readme", "First run")),
    ("modos", "comecar", "Modos de permissão", "Permission modes", ("readme", "Modos de permissão"), ("readme", "Permission modes")),
    ("headless", "uso", "Headless (-p)", "Headless (-p)", ("readme", "Headless (`-p`)"), ("readme", "Headless (`-p`)")),
    ("pronto-honesto", "uso", "“Pronto” honesto", "Honest “done”", ("readme", "\"Pronto\" honesto"), ("readme", "Honest \"done\"")),
    ("comandos", "uso", "Comandos", "Commands", ("readme", "Comandos"), None),
    ("scripts-offline", "uso", "Scripts, atalhos e modo offline", "Scripts, shortcuts and offline mode", ("readme", "Scripts, atalhos e modo offline"), None),
    ("perguntas-worktrees", "uso", "Perguntas e worktrees", "Questions and worktrees", ("readme", "Perguntas e worktrees"), None),
    ("omni", "uso", "Como o omni roteia", "How omni routes", ("readme", "Como o omni roteia"), None),
    ("configuracao", "uso", "Configuração, MCP, hooks e skills", "Configuration, MCP, hooks and skills", ("readme", "Configuração"), ("readme", "MCP, hooks, skills")),
    ("problemas", "uso", "Problemas comuns", "Troubleshooting", ("readme", "Problemas comuns"), None),
    ("evals", "uso", "Evals", "Evals", ("readme", "Evals"), ("readme", "Evals")),
    ("seguranca", "seguranca", "Segurança (resumo)", "Security (summary)", ("readme", "Segurança"), ("readme", "Security")),
    ("politica-seguranca", "seguranca", "Política de segurança", "Security policy", ("file", "SECURITY.md"), None),
    ("governanca", "seguranca", "Governança e auditoria", "Governance and audit", ("file", "docs/governance.md"), None),
    ("harness", "dentro", "Como o harness trabalha", "How the harness works", ("readme", "Como o harness trabalha"), None),
    ("capacidades", "dentro", "O que o jazz pode fazer na máquina", "What jazz can do on your machine", ("readme", "O que o jazz pode fazer na sua máquina"), None),
    ("arquitetura", "dentro", "Arquitetura", "Architecture", ("file", "docs/ARCHITECTURE.md"), ("file", "docs/ARCHITECTURE.en.md")),
    ("ux", "dentro", "A experiência interativa", "The interactive experience", ("file", "docs/UX.md"), None),
    ("observabilidade", "dentro", "Observabilidade", "Observability", ("file", "docs/observability.md"), None),
    ("improve", "dentro", "jazz improve", "jazz improve", ("file", "docs/improve.md"), None),
    ("changelog", "projeto", "Changelog", "Changelog", ("file", "CHANGELOG.md"), None),
]
# arquivo do repositório -> página da documentação
FILE_PAGE = {"SECURITY.md": "politica-seguranca", "CHANGELOG.md": "changelog",
             "docs/ARCHITECTURE.md": "arquitetura", "docs/ARCHITECTURE.en.md": "arquitetura",
             "docs/UX.md": "ux", "docs/observability.md": "observabilidade",
             "docs/governance.md": "governanca", "docs/improve.md": "improve"}

LANDING = {
    "pt": """# Documentação do jazz-cli

O jazz é um agente de código para o terminal, em Go, num binário estático único. Esta documentação
é gerada dos Markdown do repositório; cada página tem um link para a fonte no GitHub. Use a busca
(atalho `/`) para achar um título.

## Comece aqui

Quatro passos, na ordem.

### 1. Instalar

Linux e macOS (o instalador confere a assinatura e o sha256 e instala em `~/.local/bin`, sem sudo):

```sh
curl -fsSL https://github.com/jazzautomations/jazz-cli/releases/latest/download/install.sh | sh
```

Windows (PowerShell), Termux, Linux 32 bits e compilar do código estão em [Instalação](instalacao).

### 2. Primeira execução

```sh
jazz doctor    # diagnóstico: provedores, shell, sandbox, terminal
jazz           # modo interativo na pasta atual
```

Sem chave nenhuma o jazz já funciona, com modelos grátis. Detalhes em [Primeiro uso](primeiro-uso).

### 3. Modos de permissão

`turbo` (com sandbox do sistema), `auto`, `plan`, `ask`, `smart` e `yolo`. Shift+Tab alterna o modo
na sessão. Em repositório desconhecido, use `ask`. Veja [Modos de permissão](modos) e a
[Segurança](seguranca).

### 4. Modelos

```sh
jazz key openrouter                # salva uma chave (vale pra qualquer provedor)
jazz omni setup                    # liga o roteador entre provedores grátis
jazz -m groq/openai/gpt-oss-120b   # escolhe um modelo
```

O [omni](omni) distribui os pedidos entre os provedores, com os grátis na frente. Em
[Primeiro uso](primeiro-uso) estão ChatGPT por login, modelos locais e Ollama.

## Todas as páginas
""",
    "en": """# jazz-cli documentation

jazz is a coding agent for the terminal, written in Go, shipped as a single static binary. These
pages are generated from the repository's Markdown; each one links to its source on GitHub. Use the
search box (shortcut `/`) to find a heading. Pages without an English version show the Portuguese
text with a notice.

## Start here

Four steps, in order.

### 1. Install

Linux and macOS (the installer verifies the signature and sha256 and installs to `~/.local/bin`, no sudo):

```sh
curl -fsSL https://github.com/jazzautomations/jazz-cli/releases/latest/download/install.sh | sh
```

Windows (PowerShell), Termux, 32-bit Linux and building from source are on the [Install](instalacao) page.

### 2. First run

```sh
jazz doctor    # diagnostics: providers, shell, sandbox, terminal
jazz           # interactive mode in the current folder
```

jazz works with no key at all, on free models. Details in [First run](primeiro-uso).

### 3. Permission modes

`turbo` (with the system sandbox), `auto`, `plan`, `ask`, `smart` and `yolo`. Shift+Tab switches the
mode inside a session. For an unfamiliar repository, use `ask`. See [Permission modes](modos) and
[Security](seguranca).

### 4. Models

```sh
jazz key openrouter                # saves a key (works for any provider)
jazz omni setup                    # turns on the router across free providers
jazz -m groq/openai/gpt-oss-120b   # pick a model
```

[omni](omni) spreads requests across providers, free ones first. [First run](primeiro-uso) covers
ChatGPT login, local models and Ollama.

## All pages
""",
}


# ---------------------------------------------------------------- Markdown

def esc(s):
    return html.escape(s, quote=False)


def attr(s):
    return html.escape(s, quote=True)


def plain(s):
    s = re.sub(r"!?\[([^\]]*)\]\([^)]*\)", r"\1", s)
    return re.sub(r"[`*]|(?<!\w)_|_(?!\w)", "", s).strip()


def slugify(text):
    t = plain(text).lower()
    t = re.sub(r"[^\w\s-]", "", t, flags=re.UNICODE)
    return re.sub(r"\s", "-", t.strip()) or "secao"


class Ctx:
    """Contexto de uma renderização: idioma, arquivo de origem, mapa de âncoras."""

    def __init__(self, lang, src, page, anchors):
        self.lang, self.src, self.page, self.anchors = lang, src, page, anchors
        self.seen = {}
        self.headings = []  # (nível, texto, id)
        self.mermaid = False
        self.links = []  # links internos, para a checagem

    def uid(self, base):
        n = self.seen.get(base, 0)
        self.seen[base] = n + 1
        return base if n == 0 else "%s-%d" % (base, n)


def resolve(url, ctx):
    """Reescreve um link do Markdown: páginas da documentação ou GitHub."""
    if re.match(r"^[a-z][a-z0-9+.-]*:", url, re.I):
        return url
    path, _, frag = url.partition("#")
    amap = ctx.anchors[ctx.lang]
    if not path:
        if not frag:
            return "#"
        host = amap.get(frag)
        if host is None:
            return "%s/blob/main/%s#%s" % (GH, ctx.src, frag)
        return ("#" + frag) if host == ctx.page else "/docs/%s#%s" % (host, frag)
    if ctx.page == "index" and path in AVAILABLE:  # a página inicial usa os slugs direto
        return "/docs/" + path
    rel = os.path.normpath(os.path.join(os.path.dirname(ctx.src), path)).replace(os.sep, "/")
    if rel in ("README.md",):
        # a âncora decide a página; sem âncora, visão geral
        target = amap.get(frag) if frag else None
        if target:
            return ("#" + frag) if target == ctx.page else "/docs/%s#%s" % (target, frag)
        return "/docs/sobre"
    if rel in FILE_PAGE and FILE_PAGE[rel] in AVAILABLE:
        target = FILE_PAGE[rel]
        if frag and amap.get(frag) != target:
            frag = ""
        return "/docs/" + target + ("#" + frag if frag else "")
    kind = "tree" if os.path.isdir(os.path.join(ROOT, rel)) else "blob"
    return "%s/%s/main/%s%s" % (GH, kind, rel, "#" + frag if frag else "")


def inline(s, ctx):
    stash = []

    def put(h):
        stash.append(h)
        return "\x00%d\x01" % (len(stash) - 1)

    s = re.sub(r"(`+)(.+?)\1", lambda m: put("<code>%s</code>" % esc(m.group(2).strip())), s)
    s = re.sub(r"\\([\\`*_{}\[\]()#+\-.!|<>])", lambda m: put(esc(m.group(1))), s)
    s = esc(s)

    def emph(t):
        t = re.sub(r"\*\*(?!\s)(.+?)(?<!\s)\*\*", r"<strong>\1</strong>", t)
        t = re.sub(r"(?<![\w*])\*(?![\s*])(.+?)(?<![\s*])\*(?![\w*])", r"<em>\1</em>", t)
        t = re.sub(r"(?<!\w)_(?![\s_])(.+?)(?<![\s_])_(?!\w)", r"<em>\1</em>", t)
        return t

    def link(m):
        href = resolve(html.unescape(m.group(2)), ctx)
        if href.startswith("/docs") or href.startswith("#"):
            ctx.links.append(href)
            return put('<a href="%s">%s</a>' % (attr(href), emph(m.group(1))))
        return put('<a href="%s" rel="noopener">%s</a>' % (attr(href), emph(m.group(1))))

    s = re.sub(r"\[([^\]]+)\]\(([^)\s]+)(?:\s+&quot;[^)]*&quot;)?\)", link, s)
    s = re.sub(r"&lt;(https?://[^\s&]+)&gt;", lambda m: put('<a href="%s" rel="noopener">%s</a>' % (attr(m.group(1)), esc(m.group(1)))), s)
    s = emph(s)
    for _ in range(4):
        s = re.sub(r"\x00(\d+)\x01", lambda m: stash[int(m.group(1))], s)
    return s


ITEM = re.compile(r"^(\s*)([-*+]|\d{1,3}[.)])(\s+)(.*)$")
FENCE = re.compile(r"^(\s*)(`{3,}|~{3,})\s*([\w+-]*)")
HEAD = re.compile(r"^(#{1,6})\s+(.*?)\s*#*\s*$")
HR = re.compile(r"^\s{0,3}([-*_])(\s*\1){2,}\s*$")
SEP = re.compile(r"^\s*\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)*\|?\s*$")


def split_row(line):
    line = line.strip()
    if line.startswith("|"):
        line = line[1:]
    if line.endswith("|") and not line.endswith("\\|"):
        line = line[:-1]
    cells, cur, tick, i = [], "", False, 0
    while i < len(line):
        c = line[i]
        if c == "`":
            tick = not tick
        if c == "\\" and i + 1 < len(line) and line[i + 1] == "|":
            cur += "|" if tick else "\\|"
            i += 2
            continue
        if c == "|" and not tick:
            cells.append(cur.strip())
            cur = ""
        else:
            cur += c
        i += 1
    cells.append(cur.strip())
    return cells


def indent_of(line):
    return len(line) - len(line.lstrip(" "))


def is_table(lines, i):
    return "|" in lines[i] and i + 1 < len(lines) and "-" in lines[i + 1] and bool(SEP.match(lines[i + 1]))


def starts_block(lines, i):
    ln = lines[i]
    if FENCE.match(ln) or HEAD.match(ln) or HR.match(ln) or ln.lstrip().startswith(">"):
        return True
    m = ITEM.match(ln)
    if m and (m.group(2) in "-*+" or m.group(2).startswith("1")):
        return True
    return is_table(lines, i)


def blocks(lines, ctx, shift=0):
    out = []
    i, n = 0, len(lines)
    while i < n:
        ln = lines[i]
        if not ln.strip():
            i += 1
            continue
        m = FENCE.match(ln)
        if m:
            ind, mark, lang = len(m.group(1)), m.group(2), m.group(3)
            i += 1
            body = []
            while i < n and not (set(lines[i].strip()) == {mark[0]} and len(lines[i].strip()) >= len(mark)):
                body.append(lines[i][ind:] if lines[i][:ind].strip() == "" else lines[i])
                i += 1
            i += 1
            code = "\n".join(body)
            if lang == "mermaid":
                ctx.mermaid = True
                out.append('<pre class="mermaid" data-src="%s">%s</pre>' % (attr(code), esc(code)))
            else:
                out.append('<pre class="code mono"><code>%s</code></pre>' % esc(code))
            continue
        m = HEAD.match(ln)
        if m:
            lvl = max(1, min(6, len(m.group(1)) - shift))
            text = m.group(2)
            hid = ctx.uid(slugify(text))
            if lvl <= 3:
                ctx.headings.append((lvl, plain(text), hid))
            out.append('<h%d id="%s">%s<a class="anchor" href="#%s" aria-label="link">#</a></h%d>' % (lvl, hid, inline(text, ctx), hid, lvl))
            i += 1
            continue
        if HR.match(ln):
            out.append("<hr>")
            i += 1
            continue
        if ln.lstrip().startswith(">"):
            q = []
            while i < n and lines[i].lstrip().startswith(">"):
                q.append(re.sub(r"^\s*> ?", "", lines[i]))
                i += 1
            out.append("<blockquote>%s</blockquote>" % "".join(blocks(q, ctx)))
            continue
        if is_table(lines, i):
            head = split_row(ln)
            aligns = []
            for c in split_row(lines[i + 1]):
                aligns.append("center" if c.startswith(":") and c.endswith(":") else "right" if c.endswith(":") else "")
            i += 2
            rows = []
            while i < n and lines[i].strip() and "|" in lines[i]:
                rows.append(split_row(lines[i]))
                i += 1

            def cell(tag, txt, k):
                a = ' style="text-align:%s"' % aligns[k] if k < len(aligns) and aligns[k] else ""
                return "<%s%s>%s</%s>" % (tag, a, inline(txt, ctx), tag)

            t = '<div class="tbl"><table><thead><tr>%s</tr></thead><tbody>' % "".join(cell("th", c, k) for k, c in enumerate(head))
            for r in rows:
                t += "<tr>%s</tr>" % "".join(cell("td", c, k) for k, c in enumerate(r))
            out.append(t + "</tbody></table></div>")
            continue
        m = ITEM.match(ln)
        if m:
            base = len(m.group(1))
            ordered = m.group(2)[0].isdigit()
            start = int(re.match(r"\d+", m.group(2)).group(0)) if ordered else 1
            items = []
            while i < n:
                m = ITEM.match(lines[i])
                if not m or len(m.group(1)) != base or (m.group(2)[0].isdigit() != ordered):
                    break
                gap = len(m.group(3)) if len(m.group(3)) <= 4 else 1
                cind = base + len(m.group(2)) + gap
                body = [m.group(4)]
                i += 1
                while i < n:
                    cur = lines[i]
                    if not cur.strip():
                        j = i
                        while j < n and not lines[j].strip():
                            j += 1
                        if j < n and indent_of(lines[j]) >= cind:
                            body.extend([""] * (j - i))
                            i = j
                            continue
                        break
                    if indent_of(cur) >= cind:
                        body.append(cur[cind:])
                        i += 1
                        continue
                    if body[-1].strip() and not starts_block(lines, i) and not ITEM.match(cur):
                        body.append(cur.strip())  # continuação preguiçosa
                        i += 1
                        continue
                    break
                items.append(body)
                if i < n and not lines[i].strip():
                    j = i
                    while j < n and not lines[j].strip():
                        j += 1
                    nm = ITEM.match(lines[j]) if j < n else None
                    if nm and len(nm.group(1)) == base:
                        i = j
                    else:
                        break
            tag = "ol" if ordered else "ul"
            lis = []
            for body in items:
                parts = blocks(body, ctx)
                if parts and parts[0].startswith("<p>"):
                    parts[0] = parts[0][3:-4]
                lis.append("<li>%s</li>" % "".join(parts))
            out.append("<%s%s>%s</%s>" % (tag, ' start="%d"' % start if ordered and start != 1 else "", "".join(lis), tag))
            continue
        para = [ln]
        i += 1
        while i < n and lines[i].strip() and not starts_block(lines, i):
            para.append(lines[i])
            i += 1
        txt = "\n".join(p.strip() for p in para)
        out.append("<p>%s</p>" % inline(txt, ctx))
    return out


def render(md, ctx, shift=0):
    return "\n".join(blocks(md.split("\n"), ctx, shift))


# ---------------------------------------------------------------- fontes

def read(rel):
    with open(os.path.join(ROOT, rel), encoding="utf-8") as f:
        return f.read()


def readme_sections(rel):
    secs, intro, cur, infence = {}, [], None, False
    for ln in read(rel).split("\n"):
        if re.match(r"^\s*(```|~~~)", ln):
            infence = not infence
        if not infence and ln.startswith("## "):
            cur = ln[3:].strip()
            secs[cur] = [ln]
            continue
        (secs[cur] if cur else intro).append(ln)
    return {k: "\n".join(v) for k, v in secs.items()}, "\n".join(intro)


def get_source(spec, lang):
    """Devolve (markdown, deslocamento de nível, arquivo de origem) ou None."""
    if spec is None:
        return None
    readme = "README.md" if lang == "pt" else "README.en.md"
    if spec[0] == "readme":
        secs, _ = readme_sections(readme)
        if spec[1] not in secs:
            raise SystemExit("seção não encontrada em %s: %r" % (readme, spec[1]))
        return secs[spec[1]], 1, readme
    if spec[0] == "readme-intro":
        _, intro = readme_sections(readme)
        intro = re.sub(r"```.*?```\n?", "", intro, count=1, flags=re.S)  # tira o banner ASCII
        intro = re.sub(r"^\*\[[^\n]*\]\([^\n]*\)\*\n", "", intro, flags=re.M)
        return intro, 0, readme
    if not os.path.exists(os.path.join(ROOT, spec[1])):
        return None
    txt = re.sub(r"^\*\[[^\n]*\]\([^\n]*\.md\)\*\n", "", read(spec[1]), flags=re.M)  # link PT/EN do topo
    return txt, 0, spec[1]


# ---------------------------------------------------------------- páginas

AVAILABLE = set()


def span(pt, en):
    return '<span data-l="pt">%s</span><span data-l="en" lang="en">%s</span>' % (esc(pt), esc(en))


def sidebar(cur, heads):
    h = ['<nav class="side-nav" id="side-nav" aria-label="Documentação">']
    on = cur == "index"
    h.append('<a class="side-home%s" href="/docs"%s>%s</a>' % (" cur" if on else "", ' aria-current="page"' if on else "", span("Comece aqui", "Start here")))
    for gid, gpt, gen in GROUPS:
        pages = [p for p in PAGES if p[1] == gid and p[0] in AVAILABLE]
        if not pages:
            continue
        h.append('<p class="side-h mono">%s</p><ul>' % span(gpt, gen))
        for p in pages:
            on = p[0] == cur
            h.append('<li><a href="/docs/%s"%s>%s</a>' % (p[0], ' aria-current="page" class="cur"' if on else "", span(p[2], p[3])))
            if on and heads:
                for lang in ("pt", "en"):
                    hs = [x for x in heads.get(lang, []) if x[0] == 2]
                    if hs:
                        h.append('<ul class="side-sub" data-l="%s"%s>%s</ul>' % (lang, ' lang="en"' if lang == "en" else "", "".join('<li><a href="#%s">%s</a></li>' % (x[2], esc(x[1])) for x in hs)))
            h.append("</li>")
        h.append("</ul>")
    h.append("</nav>")
    return "\n".join(h)


HEADER = """<header class="site-header">
  <div class="wrap header-row">
    <a class="brand" href="/" aria-label="jazz-cli">
      <svg class="brand-mark" viewBox="0 0 64 64" aria-hidden="true"><rect width="64" height="64" rx="14" class="bm-bg"/><path d="M15 21l11 11-11 11" class="bm-chev"/><circle cx="42" cy="15.5" r="3.6" class="bm-dot"/><path d="M42 24v19.5a8 8 0 0 1-8 8h-2.5" class="bm-j"/></svg>
      <span class="brand-name">jazz-cli</span>
    </a>
    <nav class="nav" aria-label="Site">
      <a href="/"><span data-l="pt">Início</span><span data-l="en" lang="en">Home</span></a>
      <a href="/docs" aria-current="page">Docs</a>
      <a href="https://github.com/jazzautomations/jazz-cli">GitHub</a>
    </nav>
    <div class="header-tools">
      <button class="tool-btn lang-btn" type="button" id="lang-toggle" aria-label="Mudar idioma para inglês / Switch language">
        <span class="lang-opt" data-opt="pt">PT</span><span class="lang-sep" aria-hidden="true">/</span><span class="lang-opt" data-opt="en">EN</span>
      </button>
      <button class="tool-btn icon-btn" type="button" id="theme-toggle" aria-label="Alternar tema claro/escuro" aria-pressed="false">
        <svg class="i-moon" viewBox="0 0 24 24" aria-hidden="true"><path d="M20 14.5A8 8 0 0 1 9.5 4a8 8 0 1 0 10.5 10.5z"/></svg>
        <svg class="i-sun" viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4"/></svg>
      </button>
      <a class="tool-btn icon-btn" href="https://github.com/jazzautomations/jazz-cli" aria-label="jazz-cli no GitHub">
        <svg viewBox="0 0 24 24" aria-hidden="true" class="i-fill"><path d="M12 .5a11.5 11.5 0 0 0-3.64 22.41c.58.1.79-.25.79-.56v-2c-3.2.7-3.88-1.37-3.88-1.37-.52-1.33-1.28-1.69-1.28-1.69-1.04-.71.08-.7.08-.7 1.16.08 1.77 1.19 1.77 1.19 1.03 1.76 2.7 1.25 3.36.96.1-.75.4-1.25.73-1.54-2.56-.29-5.24-1.28-5.24-5.69 0-1.26.45-2.29 1.19-3.1-.12-.29-.52-1.46.11-3.05 0 0 .97-.31 3.17 1.18a11 11 0 0 1 5.77 0c2.2-1.49 3.17-1.18 3.17-1.18.63 1.59.23 2.76.11 3.05.74.81 1.19 1.84 1.19 3.1 0 4.42-2.69 5.39-5.25 5.68.41.36.78 1.06.78 2.14v3.17c0 .31.21.67.8.56A11.5 11.5 0 0 0 12 .5z"/></svg>
      </a>
    </div>
  </div>
</header>"""

SEARCH = """<div class="search" role="search">
  <label class="sr" for="doc-search"><span data-l="pt">Buscar na documentação</span><span data-l="en" lang="en">Search the docs</span></label>
  <input id="doc-search" type="search" autocomplete="off" spellcheck="false" data-ph-pt="Buscar títulos…  ( / )" data-ph-en="Search headings…  ( / )" aria-controls="search-results" aria-expanded="false">
  <ul id="search-results" class="results" hidden></ul>
  <p class="sr" id="search-status" role="status" aria-live="polite"></p>
</div>"""

PAGE = """<!doctype html>
<html lang="pt-BR" data-lang="pt" data-theme="dark" class="no-js">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>%(title)s · jazz-cli docs</title>
<meta name="description" content="%(desc)s">
<meta name="color-scheme" content="dark light">
<meta name="theme-color" content="#242933">
<link rel="canonical" href="%(canon)s">
<link rel="icon" href="/assets/favicon.svg" type="image/svg+xml">
<link rel="icon" href="/assets/favicon-32.png" sizes="32x32" type="image/png">
<link rel="apple-touch-icon" href="/assets/apple-touch-icon.png">
<meta property="og:type" content="article">
<meta property="og:site_name" content="jazz-cli docs">
<meta property="og:title" content="%(title)s · jazz-cli docs">
<meta property="og:description" content="%(desc)s">
<meta property="og:url" content="%(canon)s">
<meta property="og:image" content="%(base)s/assets/og.png">
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link rel="stylesheet" href="https://fonts.googleapis.com/css2?family=IBM+Plex+Sans:wght@400;500;600;700&family=JetBrains+Mono:wght@400;500;700&display=swap">
<link rel="stylesheet" href="/styles.css">
<link rel="stylesheet" href="/docs/docs.css">
<script src="/boot.js"></script>
%(mer)s<script src="/docs/docs.js" defer></script>
</head>
<body data-title-pt="%(tpt)s" data-title-en="%(ten)s">
<a class="skip" href="#main"><span data-l="pt">Pular para o conteúdo</span><span data-l="en" lang="en">Skip to content</span></a>
%(header)s
<div class="wrap docs-layout">
  <aside class="sidebar" id="sidebar">
    <button class="side-toggle" type="button" id="side-toggle" aria-expanded="false" aria-controls="side-body"><span data-l="pt">Menu da documentação</span><span data-l="en" lang="en">Docs menu</span></button>
    <div class="side-body" id="side-body">
%(search)s
%(side)s
    </div>
  </aside>
  <main id="main" class="doc">
%(body)s
%(srcl)s
%(pn)s
  </main>
</div>
</body>
</html>
"""


def page_html(slug, title, title_en, desc, body, side, mermaid, src_url, prev, nxt):
    mer = '<script src="%s" integrity="%s" crossorigin="anonymous" defer></script>\n' % MERMAID if mermaid else ""
    canon = BASE + ("/docs" if slug == "index" else "/docs/" + slug)

    def nav(p, pt, en, cls):
        if not p:
            return "<span></span>"
        return '<a class="%s" href="/docs/%s"><small>%s</small><span>%s</span></a>' % (cls, p[0], span(pt, en), span(p[2], p[3]))

    pn = ""
    if prev or nxt:
        pn = '<nav class="pn" aria-label="Páginas">%s%s</nav>' % (nav(prev, "← anterior", "← previous", "prev"), nav(nxt, "próxima →", "next →", "next"))
    srcl = ""
    if src_url:
        srcl = ('<p class="srcline"><a href="%s" rel="noopener"><span data-l="pt">Ver a fonte no GitHub</span>'
                '<span data-l="en" lang="en">View the source on GitHub</span></a></p>' % attr(src_url))
    return PAGE % dict(title=attr(title), desc=attr(desc), canon=canon, base=BASE, mer=mer, header=HEADER, search=SEARCH,
                       side=side, body=body, srcl=srcl, pn=pn, tpt=attr(title + " · jazz-cli docs"),
                       ten=attr(title_en + " · jazz-cli docs"))


def first_para(h):
    m = re.search(r"<p>(.*?)</p>", h, re.S)
    t = re.sub(r"<[^>]+>", "", m.group(1)) if m else ""
    t = html.unescape(re.sub(r"\s+", " ", t)).strip()
    return (t[:197] + "...") if len(t) > 200 else t


def build(write=True):
    sources = {}
    for p in PAGES:
        pt = get_source(p[4], "pt")
        if pt is None:
            print("pulada (fonte ausente): %s" % p[0])
            continue
        sources[p[0]] = (pt, get_source(p[5], "en"))
        AVAILABLE.add(p[0])
    order = [p for p in PAGES if p[0] in sources]

    # 1) passada de âncoras (qual página tem qual título)
    anchors = {"pt": {}, "en": {}}
    for lang in ("pt", "en"):
        for p in order:
            pt, en = sources[p[0]]
            md, shift, src = en if (lang == "en" and en) else pt
            c = Ctx(lang, src, p[0], {"pt": {}, "en": {}})
            render(md, c, shift)
            for _, _, hid in c.headings:
                anchors[lang].setdefault(hid, p[0])

    # 2) renderização final
    out, search, all_links = {}, [], []
    for idx, p in enumerate(order):
        slug = p[0]
        pt, en = sources[slug]
        rendered, heads, mer = {}, {}, False
        for lang in ("pt", "en"):
            if lang == "en" and en is None:
                continue
            md, shift, src = en if lang == "en" else pt
            c = Ctx(lang, src, slug, anchors)
            rendered[lang] = render(md, c, shift)
            heads[lang] = c.headings
            all_links += [(slug, h, lang) for h in c.links]
            mer = mer or c.mermaid
        if en is not None:
            body = '<div class="doc-body" data-l="pt">%s</div>\n<div class="doc-body" data-l="en" lang="en">%s</div>' % (rendered["pt"], rendered["en"])
        else:
            body = ('<p class="notice-en" data-l="en" lang="en">This page is only available in Portuguese.</p>\n'
                    '<div class="doc-body">%s</div>' % rendered["pt"])
            heads["en"] = heads["pt"]
        for lang in (("pt", "en") if en is not None else ("both",)):
            hs = heads["pt" if lang == "both" else lang]
            ptitle = p[3] if lang == "en" else p[2]
            search.append({"u": "/docs/" + slug, "t": ptitle, "h": "", "l": lang})
            for _, text, hid in hs:
                search.append({"u": "/docs/%s#%s" % (slug, hid), "t": ptitle, "h": text, "l": lang})
        out[slug] = (p, body, heads, mer, "%s/blob/main/%s" % (GH, pt[2]),
                     order[idx - 1] if idx else None, order[idx + 1] if idx + 1 < len(order) else None,
                     first_para(rendered["pt"]) or p[2])

    # 3) página inicial
    parts, lheads = {}, {}
    for lang in ("pt", "en"):
        c = Ctx(lang, "README.md", "index", anchors)
        listing = ""
        for gid, gpt, gen in GROUPS:
            ps = [x for x in order if x[1] == gid]
            if ps:
                listing += "\n### %s\n\n" % (gpt if lang == "pt" else gen)
                listing += "".join("- [%s](%s)\n" % (x[2 if lang == "pt" else 3], x[0]) for x in ps)
        parts[lang] = render(LANDING[lang] + listing, c, 0)
        all_links += [("index", h, lang) for h in c.links]
        lheads[lang] = c.headings
        for _, text, hid in c.headings:
            search.append({"u": "/docs#" + hid, "t": "Comece aqui" if lang == "pt" else "Start here", "h": text, "l": lang})
    land_body = '<div class="doc-body" data-l="pt">%s</div>\n<div class="doc-body" data-l="en" lang="en">%s</div>' % (parts["pt"], parts["en"])

    # 4) links internos
    ids = {s: set(re.findall(r'id="([^"]+)"', v[1])) for s, v in out.items()}
    ids["index"] = set(re.findall(r'id="([^"]+)"', land_body))
    broken = []
    for slug, href, lang in all_links:
        path, _, frag = href.partition("#")
        if not path:
            ok = frag in ids[slug]
        else:
            tgt = "index" if path == "/docs" else path[len("/docs/"):]
            ok = tgt in ids and (not frag or frag in ids[tgt])
        if not ok:
            broken.append((slug, href))

    if write:
        os.makedirs(OUT, exist_ok=True)
        keep = {"index.html"}
        with open(os.path.join(OUT, "index.html"), "w", encoding="utf-8") as f:
            f.write(page_html("index", "Documentação", "Documentation",
                              "Documentação do jazz-cli: instalação, primeiro uso, modos de permissão, segurança, arquitetura e mais.",
                              land_body, sidebar("index", lheads), False, None, None, order[0]))
        for slug, (p, body, heads, mer, src_url, prev, nxt, desc) in out.items():
            with open(os.path.join(OUT, slug + ".html"), "w", encoding="utf-8") as f:
                f.write(page_html(slug, p[2], p[3], desc, body, sidebar(slug, heads), mer, src_url, prev, nxt))
            keep.add(slug + ".html")
        for fn in os.listdir(OUT):
            if fn.endswith(".html") and fn not in keep:
                os.remove(os.path.join(OUT, fn))
        with open(os.path.join(OUT, "search.json"), "w", encoding="utf-8") as f:
            json.dump(search, f, ensure_ascii=False, separators=(",", ":"))
        write_sitemap(["/docs"] + ["/docs/" + s for s in out])

    print("%d páginas + índice, %d entradas de busca, %d links internos conferidos" % (len(out), len(search), len(all_links)))
    print("com versão EN:", ", ".join(s for s, v in sources.items() if v[1] is not None))
    print("só PT:", ", ".join(s for s, v in sources.items() if v[1] is None))
    if broken:
        print("LINKS QUEBRADOS:")
        for b in sorted(set(broken)):
            print("  ", b)
        return 1
    return 0


def write_sitemap(urls):
    path = os.path.join(SITE, "sitemap.xml")
    with open(path, encoding="utf-8") as f:
        xml = f.read()
    xml = re.sub(r"\n[ \t]*<url><loc>%s/docs[^<]*</loc>.*?</url>" % re.escape(BASE), "", xml)
    day = datetime.date.today().isoformat()
    add = "".join('  <url><loc>%s%s</loc><lastmod>%s</lastmod></url>\n' % (BASE, u, day) for u in urls)
    xml = xml.replace("</urlset>", add + "</urlset>")
    with open(path, "w", encoding="utf-8") as f:
        f.write(xml)


if __name__ == "__main__":
    sys.exit(build(write="--check" not in sys.argv))
