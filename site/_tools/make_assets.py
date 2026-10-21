#!/usr/bin/env python3
"""Gera as imagens raster do site (og.png 1200x630, favicon-32.png, apple-touch-icon.png).

Uso (do diretório site/):  python3 _tools/make_assets.py
Precisa de Pillow e das fontes Menlo/Helvetica Neue do macOS (troque FONT_* em outro sistema).
As versões vetoriais (assets/og.svg, assets/favicon.svg) são escritas à mão e não dependem disto.
"""
import os
from PIL import Image, ImageDraw, ImageFont

HERE = os.path.dirname(os.path.abspath(__file__))
OUT = os.path.join(HERE, "..", "assets")

FONT_MONO = "/System/Library/Fonts/Menlo.ttc"
FONT_SANS = "/System/Library/Fonts/HelveticaNeue.ttc"

BG = (36, 41, 51)        # #242933
PANEL = (46, 52, 64)     # #2E3440
LINE = (67, 76, 94)      # #434C5E
FG = (236, 239, 244)     # #ECEFF4
MUTED = (167, 177, 194)  # #A7B1C2
FROST = (136, 192, 208)  # #88C0D0
SHADOW = (94, 129, 172)  # #5E81AC
GREEN = (163, 190, 140)  # #A3BE8C
GOLD = (235, 203, 139)   # #EBCB8B

BANNER = [
    "     ██╗ █████╗ ███████╗███████╗",
    "     ██║██╔══██╗╚══███╔╝╚══███╔╝",
    "     ██║███████║  ███╔╝   ███╔╝ ",
    "██   ██║██╔══██║ ███╔╝   ███╔╝  ",
    "╚█████╔╝██║  ██║███████╗███████╗",
    " ╚════╝ ╚═╝  ╚═╝╚══════╝╚══════╝",
]


def font(path, size, index=0):
    return ImageFont.truetype(path, size, index=index)


def draw_banner(d, x, y, size):
    f = font(FONT_MONO, size, 0)  # Menlo Regular: o Bold não tem os caracteres de caixa
    cw = f.getlength("█")
    top, bottom = f.getbbox("█")[1], f.getbbox("█")[3]
    lh = bottom - top
    for i, row in enumerate(BANNER):
        for j, ch in enumerate(row):
            if ch == " ":
                continue
            col = FROST if ch == "█" else SHADOW
            d.text((x + j * cw, y + i * lh - top), ch, font=f, fill=col)
    return cw * len(BANNER[0]), lh * len(BANNER)


def og():
    W, H = 1200, 630
    im = Image.new("RGB", (W, H), BG)
    d = ImageDraw.Draw(im)
    # grade discreta
    for gx in range(0, W, 40):
        d.line([(gx, 0), (gx, H)], fill=(40, 45, 56))
    for gy in range(0, H, 40):
        d.line([(0, gy), (W, gy)], fill=(40, 45, 56))
    # janela de terminal
    x0, y0, x1, y1 = 64, 64, W - 64, H - 64
    d.rounded_rectangle([x0, y0, x1, y1], radius=18, fill=PANEL, outline=LINE, width=2)
    d.line([(x0, y0 + 48), (x1, y0 + 48)], fill=LINE, width=2)
    for k, c in enumerate([(191, 97, 106), GOLD, GREEN]):
        cx = x0 + 30 + k * 26
        d.ellipse([cx - 7, y0 + 17, cx + 7, y0 + 31], fill=c)
    mono_s = font(FONT_MONO, 20)
    d.text((x0 + 120, y0 + 13), "jazz — ~/projeto", font=mono_s, fill=MUTED)

    draw_banner(d, x0 + 56, y0 + 92, 30)
    sans_b = font(FONT_SANS, 44, 1)
    sans = font(FONT_SANS, 28)
    d.text((x0 + 56, y0 + 300), "Um agente de código no terminal", font=sans_b, fill=FG)
    d.text((x0 + 56, y0 + 356), "que roda em qualquer máquina — até num Pentium M de 2005.", font=sans, fill=MUTED)
    mono = font(FONT_MONO, 22)
    yy = y0 + 420
    d.text((x0 + 56, yy), "›", font=mono, fill=GOLD)
    d.text((x0 + 84, yy), "Go · binário estático · i386 a arm64 · MIT", font=mono, fill=FG)
    d.text((x0 + 56, yy + 44), "jazz-cli.jazzautomations.com.br", font=mono, fill=FROST)
    im.save(os.path.join(OUT, "og.png"), optimize=True)


def icon(size, name):
    # mesma geometria do assets/favicon.svg (viewBox 64), desenhada em 512 e reduzida
    S, k = 512, 8
    im = Image.new("RGBA", (S, S), (0, 0, 0, 0))
    d = ImageDraw.Draw(im)
    d.rounded_rectangle([0, 0, S - 1, S - 1], radius=14 * k, fill=PANEL)
    w = int(5.5 * k)
    pts = [(15 * k, 21 * k), (26 * k, 32 * k), (15 * k, 43 * k)]
    d.line(pts, fill=GOLD, width=w, joint="curve")
    for x, y in (pts[0], pts[2]):
        d.ellipse([x - w / 2, y - w / 2, x + w / 2, y + w / 2], fill=GOLD)
    r = 3.6 * k
    d.ellipse([42 * k - r, 15.5 * k - r, 42 * k + r, 15.5 * k + r], fill=FROST)
    d.line([(42 * k, 24 * k), (42 * k, 43.5 * k)], fill=FROST, width=w)
    d.ellipse([42 * k - w / 2, 24 * k - w / 2, 42 * k + w / 2, 24 * k + w / 2], fill=FROST)
    # curva do j: quarto de círculo de raio 8 centrado em (34, 43.5)
    d.arc([26 * k - w / 2, 35.5 * k - w / 2, 42 * k + w / 2, 51.5 * k + w / 2], 0, 90, fill=FROST, width=w)  # Pillow desenha a espessura para dentro
    d.line([(34 * k, 51.5 * k), (31.5 * k, 51.5 * k)], fill=FROST, width=w)
    d.ellipse([31.5 * k - w / 2, 51.5 * k - w / 2, 31.5 * k + w / 2, 51.5 * k + w / 2], fill=FROST)
    im = im.resize((size, size), Image.LANCZOS)
    im.save(os.path.join(OUT, name), optimize=True)


if __name__ == "__main__":
    os.makedirs(OUT, exist_ok=True)
    og()
    icon(32, "favicon-32.png")
    icon(180, "apple-touch-icon.png")
    print("ok")
