# ui_lint: deterministic UI lint over changed frontend files (HTML/JSX/TSX/Vue/Svelte/Astro/CSS/SCSS).
# Regex-only, stdlib-only, Python 3.6+. Rules derived from the Impeccable detector (Apache-2.0,
# github.com/pbakaus/impeccable; see CREDITS.md and docs/research/impeccable-frontend-craft.md),
# rewritten here: only the checks a regex can decide without rendering the page.
# Waivers: "ui-lint-disable <rule>" anywhere in a file turns that rule off for the file;
# "ui-lint-disable-line" on a line skips that line.

MARKUP_EXT = (".html", ".htm", ".jsx", ".tsx", ".vue", ".svelte", ".astro")
STYLE_EXT = (".css", ".scss", ".sass", ".less")
UI_EXT = MARKUP_EXT + STYLE_EXT
MAX_BYTES = 512 << 10
SEV_ORDER = {"error": 0, "warn": 1, "info": 2}

findings = []
warnings = []


def add(file, line, rule, sev, msg):
    findings.append({"file": file, "line": int(line), "rule": rule, "severity": sev, "msg": clip(msg, 200)})


def line_at(text, pos):
    return text.count("\n", 0, pos) + 1


def as_list(v):
    if v is None:
        return []
    if isinstance(v, (list, tuple)):
        return [str(x) for x in v if str(x).strip()]
    return [s for s in re.split(r"[\s,]+", str(v)) if s]


# ---------- colour helpers ----------

HEX_RE = re.compile(r"#([0-9a-fA-F]{3,4}|[0-9a-fA-F]{6}|[0-9a-fA-F]{8})\b")
RGB_RE = re.compile(r"rgba?\(\s*(\d{1,3})[\s,]+(\d{1,3})[\s,]+(\d{1,3})")
HSL_RE = re.compile(r"hsla?\(\s*(-?\d+(?:\.\d+)?)(?:deg)?[\s,]+(\d+(?:\.\d+)?)%[\s,]+(\d+(?:\.\d+)?)%")
NAMED_HUE = {"purple": (300, 1.0, 0.25), "violet": (300, 0.76, 0.72), "indigo": (275, 1.0, 0.25),
             "blueviolet": (271, 0.76, 0.53), "mediumpurple": (260, 0.6, 0.65), "rebeccapurple": (270, 0.5, 0.4),
             "darkviolet": (282, 1.0, 0.41), "fuchsia": (300, 1.0, 0.5), "magenta": (300, 1.0, 0.5),
             "cyan": (180, 1.0, 0.5), "aqua": (180, 1.0, 0.5)}


def rgb_to_hsl(r, g, b):
    r, g, b = r / 255.0, g / 255.0, b / 255.0
    mx, mn = max(r, g, b), min(r, g, b)
    l = (mx + mn) / 2.0
    if mx == mn:
        return 0.0, 0.0, l
    d = mx - mn
    s = d / (2.0 - mx - mn) if l > 0.5 else d / (mx + mn)
    if mx == r:
        h = (g - b) / d + (6 if g < b else 0)
    elif mx == g:
        h = (b - r) / d + 2
    else:
        h = (r - g) / d + 4
    return h * 60.0, s, l


def colors_in(s):
    """(hue, saturation, lightness) of every literal colour in s."""
    out = []
    for m in HEX_RE.finditer(s):
        h = m.group(1)
        if len(h) in (3, 4):
            h = "".join(c * 2 for c in h[:3])
        h = h[:6]
        out.append(rgb_to_hsl(int(h[0:2], 16), int(h[2:4], 16), int(h[4:6], 16)))
    for m in RGB_RE.finditer(s):
        out.append(rgb_to_hsl(min(255, int(m.group(1))), min(255, int(m.group(2))), min(255, int(m.group(3)))))
    for m in HSL_RE.finditer(s):
        out.append((float(m.group(1)) % 360, float(m.group(2)) / 100.0, float(m.group(3)) / 100.0))
    for w in re.findall(r"\b([a-z]+)\b", s.lower()):
        if w in NAMED_HUE:
            out.append(NAMED_HUE[w])
    return out


def chromatic(c):
    return c[1] >= 0.3 and 0.12 <= c[2] <= 0.9


def violet(c):
    return chromatic(c) and 250 <= c[0] <= 300


def cyan(c):
    return chromatic(c) and 175 <= c[0] <= 200


# ---------- rules over CSS text (style files, <style> blocks, style="" attributes) ----------

DECL_RE = re.compile(r"(?<![\w-])(-{0,2}[a-zA-Z][\w-]*)\s*:\s*([^;{}]+)")
KEYFRAMES_RE = re.compile(r"@(?:-webkit-)?keyframes\b")
ANIM_RE = re.compile(r"(?<![\w-])animation(?:-name)?\s*:\s*(?!\s*none\b)[^;{}]+")
REDUCED_RE = re.compile(r"prefers-reduced-motion")
TW_ANIM_RE = re.compile(r"(?<![\w:-])animate-(?!none\b)[a-z][\w-]*")
TW_REDUCED_RE = re.compile(r"\bmotion-(?:safe|reduce):")
FOCUS_RE = re.compile(r":focus(?:-visible|-within)?\b|\bfocus(?:-visible|-within)?:")
HOVER_RE = re.compile(r":hover\b|\bhover:")
OUTLINE_NONE_RE = re.compile(r"(?<![\w-])outline\s*:\s*(?:none|0(?:px)?)\s*(?:!important)?\s*(?:;|$|})")
TW_OUTLINE_NONE_RE = re.compile(r"(?<![\w:-])outline-none\b")


def css_rules(rel_path, text, base_line, tokens_known, ctx):
    """Declaration-level rules. base_line: line of text[0] in the file."""
    radii = []
    for m in DECL_RE.finditer(text):
        prop, val = m.group(1).lower(), m.group(2).strip()
        ln = base_line + text.count("\n", 0, m.start())
        lval = val.lower()
        if prop in ("background-clip", "-webkit-background-clip") and re.match(r"text\b", lval):
            add(rel_path, ln, "gradient-text", "warn",
                "gradient/clipped text: give emphasis with weight or size, set text in a solid colour")
        if "gradient(" in lval and prop.startswith("background"):
            cs = colors_in(val)
            if any(violet(c) for c in cs) or (any(cyan(c) for c in cs) and len(cs) >= 2):
                add(rel_path, ln, "default-gradient", "warn",
                    "violet/indigo (or cyan) gradient is the stock generated-UI look: pick colours from the brief's palette")
        if prop in ("box-shadow", "text-shadow"):
            for part in re.split(r",(?![^(]*\))", val):
                sm = re.match(r"\s*(?:inset\s+)?0(?:px)?\s+0(?:px)?\s+(\d+(?:\.\d+)?)(?:px)?", part)
                if sm and float(sm.group(1)) >= 8 and any(chromatic(c) for c in colors_in(part)):
                    add(rel_path, ln, "glow-shadow", "warn",
                        "zero-offset coloured glow: a shadow needs an offset and a soft neutral blur, or remove it")
                    break
        if prop in ("border-left", "border-right", "border-inline-start", "border-inline-end"):
            bm = re.match(r"(\d+(?:\.\d+)?)px\s+solid\s+(.+)", lval)
            if bm and float(bm.group(1)) >= 2 and any(chromatic(c) for c in colors_in(bm.group(2))):
                add(rel_path, ln, "side-stripe", "warn",
                    "thick coloured %s on a block is a stock accent: use spacing, a heading or a background tint" % prop)
        if prop in ("animation", "animation-timing-function", "transition", "transition-timing-function"):
            if re.search(r"\b(bounce|elastic|wobble|jiggle)\b", lval):
                add(rel_path, ln, "bounce-easing", "info", "bounce/elastic motion feels dated: use an exponential ease-out")
            for cb in re.finditer(r"cubic-bezier\(\s*([-\d.]+)\s*,\s*([-\d.]+)\s*,\s*([-\d.]+)\s*,\s*([-\d.]+)\s*\)", lval):
                try:
                    y1, y2 = float(cb.group(2)), float(cb.group(4))
                except ValueError:
                    continue
                if y1 < -0.1 or y1 > 1.1 or y2 < -0.1 or y2 > 1.1:
                    add(rel_path, ln, "bounce-easing", "info", "overshooting cubic-bezier reads as bounce: ease out without overshoot")
                    break
        if prop == "font-size":
            fm = re.match(r"(\d+(?:\.\d+)?)px\b", lval)
            if fm and 0 < float(fm.group(1)) < 12:
                add(rel_path, ln, "tiny-text", "warn", "font-size %spx is below 12px: body 16px, secondary text at least 12-14px" % fm.group(1))
        if prop == "text-align" and lval.startswith("justify"):
            add(rel_path, ln, "justified-text", "info", "justified text without hyphenation opens rivers: align left")
        if prop in ("border-radius",):
            rm = re.match(r"(\d+(?:\.\d+)?)(px|rem)\s*(?:!important)?$", lval)
            if rm:
                px = float(rm.group(1)) * (16 if rm.group(2) == "rem" else 1)
                radii.append((px, ln))
        if tokens_known and not prop.startswith("--") and prop not in ("content", "src", "font-family", "grid-template-areas"):
            lit = HEX_RE.search(val) or RGB_RE.search(val) or HSL_RE.search(val)
            # neutral literals (shadow blacks, hairline greys) are not palette drift
            if lit and "var(" not in lval and any(c[1] >= 0.12 and 0.04 < c[2] < 0.97 for c in colors_in(val)):
                ctx["hard_colors"].append(ln)
    ctx["radii"].extend(radii)
    if KEYFRAMES_RE.search(text) or ANIM_RE.search(text):
        ctx["anim_line"] = ctx["anim_line"] or base_line + text.count("\n", 0, (KEYFRAMES_RE.search(text) or ANIM_RE.search(text)).start())
    if REDUCED_RE.search(text):
        ctx["reduced"] = True
    om = OUTLINE_NONE_RE.search(text)
    if om:
        ctx["outline_none"] = ctx["outline_none"] or base_line + text.count("\n", 0, om.start())
    if FOCUS_RE.search(text):
        ctx["focus"] = True
    hm = HOVER_RE.search(text)
    if hm:
        ctx["hover_line"] = ctx["hover_line"] or base_line + text.count("\n", 0, hm.start())


# ---------- rules over markup ----------

TAG_IMG_RE = re.compile(r"<img\b[^>]*>", re.I | re.S)
STYLE_BLOCK_RE = re.compile(r"(<style\b[^>]*>)(.*?)</style>", re.I | re.S)
STYLE_ATTR_RE = re.compile(r"\bstyle\s*=\s*(\"[^\"]*\"|'[^']*')", re.I)
CLASS_ATTR_RE = re.compile(r"\b(?:class|className)\s*=\s*(\"[^\"]*\"|'[^']*'|\{`[^`]*`\})", re.S)
HEADING_RE = re.compile(r"<h([1-6])\b", re.I)
SCRIPT_RE = re.compile(r"<script\b[^>]*>.*?</script>", re.I | re.S)
COMMENT_RE = re.compile(r"<!--.*?-->", re.S)
EMOJI_RE = re.compile(u"[\U0001F300-\U0001FAFF\U0001F000-\U0001F2FF☀-➿⭐⭕]")
TEXT_EL_RE = re.compile(r"<(h[1-6]|button|a|li|span|i|div|p|dt|td|th|label)\b[^>]*>([^<]{0,160})", re.I)
LOREM_RE = re.compile(r"\blorem ipsum\b|\bdolor sit amet\b|\bconsectetur adipiscing\b|\b(?:your|seu|sua) (?:text|texto|title|t[íi]tulo|logo|company|empresa) (?:here|aqui)\b|\btexto de exemplo\b|\bplaceholder text\b", re.I)
EYEBROW_RE = re.compile(r"\b(?:class|className)\s*=\s*[\"'][^\"']*\b(eyebrow|kicker|overline|pretitle|pre-title)\b", re.I)
BUZZ = ["streamline your", "empower your", "supercharge your", "unleash your", "unleash the power",
        "leverage the power", "harness the power", "best-in-class", "industry-leading", "world-class",
        "enterprise-grade", "next-generation", "cutting-edge", "transform your business", "revolutionize",
        "game-changer", "game changing", "seamless experience", "seamlessly integrate", "future-proof",
        "take your business to the next level", "elevate your",
        "leve seu negócio", "próximo nível", "revolucion", "de ponta", "solução completa",
        "soluções inovadoras", "transforme seu negócio", "potencialize", "experiência única",
        "experiência incrível", "eleve seu", "eleve sua"]
TAGS_RE = re.compile(r"<[^>]+>")


def blank(m):
    return re.sub(r"[^\n]", " ", m.group(0))


def visible_text(text):
    """text without scripts, style blocks and comments; line numbers are kept."""
    t = SCRIPT_RE.sub(blank, text)
    t = STYLE_BLOCK_RE.sub(blank, t)
    return COMMENT_RE.sub(blank, t)


def markup_rules(rel_path, text, tokens_known, ctx, is_html):
    vis = visible_text(text)
    for m in TAG_IMG_RE.finditer(text):
        tag = m.group(0)
        ln = line_at(text, m.start())
        sm = re.search(r"\bsrc\s*=\s*(\"[^\"]*\"|'[^']*'|\{)", tag, re.I)
        if sm is None and not re.search(r"\b(?:srcSet|srcset|:src|v-bind:src)\s*=", tag):
            add(rel_path, ln, "broken-image", "error", "<img> without src ships as a broken box")
        elif sm is not None and sm.group(1)[0] in "\"'" and sm.group(1)[1:-1].strip() in ("", "#"):
            add(rel_path, ln, "broken-image", "error", "<img> with an empty or '#' src ships as a broken box")
        if not re.search(r"\b(?:alt|:alt|v-bind:alt)\s*=", tag) and not re.search(r"\{\.\.\.", tag):
            add(rel_path, ln, "missing-alt", "warn", "<img> without alt: describe it, or alt=\"\" when decorative")
    # headings must not skip levels
    prev = 0
    for m in HEADING_RE.finditer(vis):
        lvl = int(m.group(1))
        if prev and lvl > prev + 1:
            add(rel_path, line_at(text, m.start()), "skipped-heading", "warn",
                "h%d after h%d skips a level: screen readers navigate by the outline" % (lvl, prev))
        prev = lvl
    # emoji standing in for icons: emoji inside headings, buttons, links, list items, small spans
    n_emoji, first = 0, 0
    for m in TEXT_EL_RE.finditer(vis):
        if EMOJI_RE.search(m.group(2)):
            n_emoji += 1
            first = first or line_at(vis, m.start())
    if n_emoji:
        add(rel_path, first, "emoji-icon", "warn",
            "%d element(s) use emoji as icons: draw icons as SVG (one stroke, one weight) or drop them" % n_emoji)
    lm = LOREM_RE.search(vis)
    if lm:
        add(rel_path, line_at(vis, lm.start()), "placeholder-copy", "warn",
            "placeholder copy (%s): write the product's real words" % lm.group(0))
    em = EYEBROW_RE.search(text)
    if em:
        add(rel_path, line_at(text, em.start()), "eyebrow-label", "info",
            "'%s' label above a heading is a stock scaffold: let the heading carry itself" % em.group(1))
    plain = TAGS_RE.sub(" ", vis).lower()
    hits = [b for b in BUZZ if b in plain]
    if hits:
        idx = vis.lower().find(hits[0])
        add(rel_path, line_at(vis, idx) if idx >= 0 else 1, "generic-copy", "info",
            "generic marketing phrase(s): %s; say what this product does, concretely" % ", ".join('"%s"' % h for h in hits[:3]))
    dashes = plain.count(u"—")
    if dashes >= 8:
        add(rel_path, 1, "em-dash-overuse", "info", "%d em-dashes in visible copy: vary the punctuation" % dashes)
    if re.search(r"<marquee\b", text, re.I):
        add(rel_path, line_at(text, re.search(r"<marquee\b", text, re.I).start()), "marquee", "warn",
            "auto-scrolling marquee hides content: let the reader move")
    if is_html and re.search(r"<html\b", text, re.I):
        hm = re.search(r"<html\b[^>]*>", text, re.I)
        if not re.search(r"\blang\s*=", hm.group(0), re.I):
            add(rel_path, line_at(text, hm.start()), "html-lang", "warn", "<html> without lang: screen readers guess the language")
        vm = re.search(r"<meta\b[^>]*name\s*=\s*[\"']viewport[\"'][^>]*>", text, re.I)
        if vm is None:
            add(rel_path, line_at(text, hm.start()), "no-viewport", "warn",
                "no <meta name=\"viewport\">: phones render the desktop layout zoomed out")
        elif re.search(r"user-scalable\s*=\s*(?:no|0)|maximum-scale\s*=\s*1(?:\.0)?\b", vm.group(0), re.I):
            add(rel_path, line_at(text, vm.start()), "zoom-blocked", "error", "viewport blocks zoom: remove user-scalable=no / maximum-scale=1")
    # utility classes (Tailwind and friends)
    for m in CLASS_ATTR_RE.finditer(text):
        cls = m.group(1)
        ln = line_at(text, m.start())
        if re.search(r"(?<![\w:-])bg-clip-text\b", cls):
            add(rel_path, ln, "gradient-text", "warn", "bg-clip-text gradient text: use a solid colour, emphasis from weight or size")
        if re.search(r"(?<![\w:-])from-(?:purple|violet|indigo|fuchsia)-\d+", cls) or \
                (re.search(r"(?<![\w:-])bg-gradient-to-\w+", cls) and re.search(r"(?<![\w:-])(?:via|to)-(?:purple|violet|indigo|fuchsia)-\d+", cls)):
            add(rel_path, ln, "default-gradient", "warn", "purple/violet/indigo gradient classes: pick colours from the brief's palette")
        if re.search(r"(?<![\w:-])border-[lr]-(?:[2-8]|\[\d+px\])\b", cls) and \
                re.search(r"(?<![\w:-])border-(?:[a-z]+)-\d{3}\b", cls):
            add(rel_path, ln, "side-stripe", "warn", "thick coloured side border: use spacing, a heading or a background tint")
        if re.search(r"(?<![\w:-])animate-bounce\b", cls):
            add(rel_path, ln, "bounce-easing", "info", "animate-bounce reads as dated: animate with an ease-out, or not at all")
        if TW_OUTLINE_NONE_RE.search(cls) and not FOCUS_RE.search(cls):
            ctx["outline_none"] = ctx["outline_none"] or ln
        for r in re.findall(r"(?<![\w:-])rounded(?:-(?:xl|2xl|3xl|full|\[\d+px\]))?\b", cls):
            ctx["tw_radii"].append((r, ln))
        if TW_ANIM_RE.search(cls):
            ctx["anim_line"] = ctx["anim_line"] or ln
        if TW_REDUCED_RE.search(cls):
            ctx["reduced"] = True
        if FOCUS_RE.search(cls):
            ctx["focus"] = True
        if HOVER_RE.search(cls):
            ctx["hover_line"] = ctx["hover_line"] or ln
    # embedded CSS
    for m in STYLE_BLOCK_RE.finditer(text):
        css_rules(rel_path, m.group(2), line_at(text, m.start(2)), tokens_known, ctx)
    for m in STYLE_ATTR_RE.finditer(text):
        css_rules(rel_path, m.group(1)[1:-1], line_at(text, m.start()), tokens_known, ctx)
    if re.search(r"\bstyled\.\w+`|\bcss`", text):  # CSS-in-JS template literals
        for m in re.finditer(r"(?:\bstyled\.\w+|\bcss)`([^`]*)`", text):
            css_rules(rel_path, m.group(1), line_at(text, m.start(1)), tokens_known, ctx)


# ---------- tokens ----------

TOKEN_DEF_RE = re.compile(r"(?m)^\s*--[\w-]+\s*:\s*(?:#[0-9a-fA-F]{3,8}\b|rgba?\(|hsla?\(|oklch\(|oklab\()")
TOKEN_NAME_RE = re.compile(r"(?:^|[/_.-])(tokens?|variables|vars|theme|colou?rs|palette|design-tokens)\.(?:css|scss|sass|less)$", re.I)


def defines_tokens(text):
    return len(TOKEN_DEF_RE.findall(text)) >= 3


def find_token_file(contents):
    for p, t in contents:
        if defines_tokens(t):
            return rel_real(p)
    n = 0
    for p in walk_files(ROOT, limit=4000):
        n += 1
        if not TOKEN_NAME_RE.search(p.replace(os.sep, "/")):
            continue
        try:
            if os.path.getsize(p) > MAX_BYTES:
                continue
            with open(p, "rb") as f:
                t = f.read().decode("utf-8", "replace")
        except (IOError, OSError):
            continue
        if defines_tokens(t):
            return rel_real(os.path.realpath(p))
    return ""


# ---------- main ----------

files = as_list(arg("files")) + as_list(arg("file"))
if not files:
    fail("pass files: the changed UI files (html, jsx, tsx, vue, svelte, astro, css, scss)")
max_findings = int(arg("max", 60))
root_abs = os.path.realpath(ROOT)


def rel_real(p):
    r = os.path.relpath(p, root_abs)
    return p if r.startswith("..") else r.replace(os.sep, "/")


contents, skipped, seen = [], [], set()
for f in files[:200]:
    p = os.path.realpath(path_in_root(f))
    if p in seen:
        continue
    seen.add(p)
    if not (p == root_abs or p.startswith(root_abs + os.sep)):
        skipped.append({"file": f, "why": "outside the project"})
        continue
    if not p.lower().endswith(UI_EXT):
        skipped.append({"file": f, "why": "not a UI file"})
        continue
    try:
        if os.path.getsize(p) > MAX_BYTES:
            skipped.append({"file": rel_real(p), "why": "larger than 512 KB"})
            continue
        with open(p, "rb") as fh:
            raw = fh.read()
    except (IOError, OSError) as e:
        skipped.append({"file": f, "why": "unreadable: %s" % e.__class__.__name__})
        continue
    if b"\0" in raw[:8000]:
        skipped.append({"file": rel_real(p), "why": "binary"})
        continue
    contents.append((p, raw.decode("utf-8", "replace").replace("\r\n", "\n")))

token_file = find_token_file(contents)
for p, text in contents:
    rp = rel_real(p)
    low = p.lower()
    start = len(findings)
    ctx = {"radii": [], "tw_radii": [], "hard_colors": [], "anim_line": 0, "reduced": False,
           "outline_none": 0, "focus": False, "hover_line": 0}
    tokens_known = bool(token_file)
    if low.endswith(STYLE_EXT):
        css = re.sub(r"/\*.*?\*/", blank, text, flags=re.S)
        css_rules(rp, css, 1, tokens_known, ctx)
    else:
        markup_rules(rp, text, tokens_known, ctx, low.endswith((".html", ".htm", ".astro")))
    # file-level rules
    if ctx["anim_line"] and not ctx["reduced"]:
        add(rp, ctx["anim_line"], "no-reduced-motion", "warn",
            "animation without a prefers-reduced-motion path: reduce movement for users who ask for it")
    if ctx["outline_none"] and not ctx["focus"]:
        add(rp, ctx["outline_none"], "focus-removed", "error",
            "outline removed with no :focus-visible style: keyboard users lose their place")
    elif ctx["hover_line"] and not ctx["focus"] and low.endswith(STYLE_EXT + (".html", ".htm")):
        add(rp, ctx["hover_line"], "no-focus-style", "warn",
            ":hover styles but no :focus-visible: give keyboard focus the same visible state")
    big = [r for r in ctx["radii"] if r[0] >= 12]
    if len(big) >= 5 and len(set(r[0] for r in big)) == 1 and len(big) == len(ctx["radii"]):
        add(rp, big[0][1], "uniform-radius", "info",
            "every one of %d border-radius values is %gpx: vary radius by element role" % (len(big), big[0][0]))
    tw_big = [r for r in ctx["tw_radii"] if r[0] in ("rounded-2xl", "rounded-3xl", "rounded-xl")]
    if len(tw_big) >= 6 and len(set(r[0] for r in tw_big)) == 1 and len(tw_big) == len(ctx["tw_radii"]):
        add(rp, tw_big[0][1], "uniform-radius", "info",
            "every rounded element uses %s (%d times): vary radius by element role" % (tw_big[0][0], len(tw_big)))
    if ctx["hard_colors"] and token_file:
        hc = sorted(set(ctx["hard_colors"]))
        add(rp, hc[0], "hardcoded-color", "info",
            "%d literal colour(s) outside the tokens in %s (lines %s): use var(--...)" %
            (len(hc), token_file, ",".join(str(x) for x in hc[:6])))
    # waivers
    off = set(re.findall(r"ui-lint-disable\s+([a-z][\w-]*)", text))
    lines = text.split("\n")
    kept = []
    for f in findings[start:]:
        if f["rule"] in off:
            continue
        if 0 < f["line"] <= len(lines) and "ui-lint-disable-line" in lines[f["line"] - 1]:
            continue
        kept.append(f)
    findings[start:] = kept

# one finding per (file, rule, line); the same rule on the next line is the same spot
uniq, keys = [], set()
for f in findings:
    k = (f["file"], f["rule"], f["line"])
    if k not in keys and (f["file"], f["rule"], f["line"] - 1) not in keys:
        uniq.append(f)
    keys.add(k)
uniq.sort(key=lambda f: (SEV_ORDER.get(f["severity"], 3), f["file"], f["line"]))
by_rule, by_sev = {}, {}
for f in uniq:
    by_rule[f["rule"]] = by_rule.get(f["rule"], 0) + 1
    by_sev[f["severity"]] = by_sev.get(f["severity"], 0) + 1
shown = uniq[:max_findings]
if len(uniq) > len(shown):
    warnings.append("%d more findings not shown" % (len(uniq) - len(shown)))
summary = {"files": len(contents), "findings": len(uniq), "errors": by_sev.get("error", 0),
           "warnings": by_sev.get("warn", 0), "info": by_sev.get("info", 0), "by_rule": by_rule,
           "token_file": token_file or None}
emit(ok=True, data={"findings": shown, "summary": summary, "skipped": skipped},
     confidence=1.0 if contents else 0.0, warnings=warnings)
