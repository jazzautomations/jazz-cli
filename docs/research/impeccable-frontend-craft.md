# Impeccable → `frontend-craft` skill + `ui_lint` hook

Source: github.com/pbakaus/impeccable (Paul Bakaus, Apache-2.0), skill version 4.7.0, detector
scripts 0.1.16, read at commit `374a0e1` (2026-10-20). Fetched with `curl` into an empty scratch
directory as data; nothing from it was executed. Files read: `.claude/skills/impeccable/SKILL.md`,
`reference/craft-floor.md`, `reference/craft.md`, `reference/mode-{persuade,operate,read}.md`,
`reference/{routing,hooks,typeset,layout,animate,colorize,harden,polish}.md`, `.claude/settings.json`
(the hook wiring), `crates/live/assets/antipatterns.json` (the rule catalogue, 61 rules),
`crates/detect/src/regex_matchers.rs` (the regex tier), `LICENSE`, `NOTICE.md`.

## What it is

A design skill for coding agents doing frontend work, plus a deterministic detector of
"generated-UI" clichés.

- **SKILL.md** (~12 KB) sets the stance (finish the job; scale ambition to the surface; verify by
  looking at the render at shipped sizes, batching the checks), a launcher setup step
  (`impeccable context` loads PRODUCT.md / DESIGN.md / surface briefs), "the brief wins",
  refinement-vs-redesign, the **four modes** (Persuade, Operate, Read, Experience, chosen from the
  surface, not the product) and a table of 24 sub-commands (`shape`, `audit`, `critique`, `polish`,
  `bolder`, `quieter`, `typeset`, `layout`, `animate`, `live`, `generate`…), each a 3–60 KB playbook.
- **craft-floor.md** is loaded right before any UI edit: a *Verify* list (contrast 4.5:1, offset
  shadows, spacing rhythm, measure 65–75ch, one authored motion moment, all states, themed browser
  surfaces, product copy, brief coverage) and a *Refuse* list (same-size icon cards, hero-metric
  template, eyebrow/kicker labels, section numbers, gradient text, decorative glass, thick side
  borders, hard offset shadows, monospace as costume, system display faces, emoji as icons, geometric
  masks, light/dark picked by category).
- **The detector** (Rust, ~1 MB of sources, plus a browser bundle) runs as a Claude Code
  `PostToolUse` hook on `Edit|Write` of UI files and as a deep pass on `Stop`. Two tiers: per edit
  only "immediate" mechanical problems (broken images, overflow, contrast, gradient text, glow,
  design-system drift); taste rules (copy cadence, palette, rhythm) are deferred to Stop. Many rules
  need a rendered DOM (contrast, overflow, line length, occlusion, cramped padding); the regex tier
  handles source-only rules.

## What we take

1. **Skill body** (`skills/frontend-craft/SKILL.md`, ~760 estimated tokens, 29 lines): a rewrite,
   not a copy. Kept: "the brief wins"; the four modes with a one-line way to decide each; the
   reflexes (hierarchy, 4px spacing scale, type scale, contrast, motion purpose + reduced motion,
   states, responsiveness, a11y basics, product copy); the bans, made generic (no font or product
   names); the finish/verify rule ("finish the deliverable; render at shipped sizes when a renderer
   exists; one look → fix the batch → look again"); and a line telling the model what to do with
   ui_lint findings. Its budget is 2× the other skills (`skBodyBudget`): it only fires on UI work.
2. **Trigger** (`skills/triggers.json`): strong words `ui|ux|front-end|landing page|dashboard|css|
   scss|tailwind|stylesheet|web page|homepage|website|redesign` and PT `design|site|painel|
   responsivo|interface do usuário`; weak words `layout|interface|tela|página|componente|estilo|html|
   botão|formulário`; negatives for API/system/database design and design docs; file globs
   `*.tsx *.jsx *.vue *.svelte *.css *.scss *.html tailwind.config.* *.astro` with phase `edit`
   (so the first UI edit of a turn picks it). Priority 45: below write-tests (a `*.test.tsx` edit
   still gets write-tests) and debug-loop, above surgical-change.
3. **Detector** (`jazz-scripts/ui_lint.py`): a reimplementation of the subset a regex can decide
   without rendering, 24 rules:

   | rule | severity | origin in Impeccable |
   |---|---|---|
   | gradient-text (`background-clip: text`, `bg-clip-text`) | warn | gradient-text |
   | default-gradient (violet/indigo hue 250–300°, or cyan, in a gradient; Tailwind `from-purple-*`) | warn | ai-color-palette |
   | glow-shadow (zero-offset chromatic shadow, blur ≥ 8px) | warn | dark-glow |
   | side-stripe (`border-left/right` ≥ 2px solid chromatic; `border-l-4 border-violet-500`) | warn | side-tab |
   | bounce-easing (bounce/elastic, overshooting cubic-bezier, `animate-bounce`) | info | bounce-easing |
   | tiny-text (font-size < 12px) | warn | tiny-text |
   | justified-text | info | justified-text |
   | uniform-radius (≥ 5 radii, all the same ≥ 12px; or ≥ 6 identical `rounded-xl/2xl/3xl`) | info | (new; the "same radius on everything" habit) |
   | hardcoded-color (chromatic literal outside `--*` when a token file with ≥ 3 colour variables exists) | info | design-system-color, simplified |
   | no-reduced-motion (`@keyframes`/`animation`/`animate-*` and no `prefers-reduced-motion`/`motion-safe:` in the file) | warn | animate.md rule |
   | focus-removed (`outline: none`/`outline-none` with no focus style) | error | polish/harden rules |
   | no-focus-style (`:hover` but no `:focus*` in a stylesheet) | warn | polish/harden rules |
   | broken-image (`<img>` without src, or src `""`/`#`) | error | broken-image |
   | missing-alt | warn | audit.md a11y |
   | skipped-heading | warn | skipped-heading |
   | emoji-icon (emoji inside headings, buttons, links, list items, spans) | warn | craft-floor ban |
   | placeholder-copy (lorem ipsum, "your text here", "texto de exemplo") | warn | craft-floor copy rule |
   | eyebrow-label (class `eyebrow`/`kicker`/`overline`) | info | kicker-above-heading |
   | generic-copy (EN + PT marketing phrases) | info | marketing-buzzword (list partly reused) |
   | em-dash-overuse (≥ 8 in visible text) | info | em-dash-overuse |
   | marquee | warn | marquee |
   | html-lang, no-viewport, zoom-blocked | warn / warn / error | audit.md |

   Input `{"files": [...]}` (paths inside the project; others are skipped with a reason), output
   `{findings: [{file,line,rule,severity,msg}], summary: {files, findings, errors, warnings, info,
   by_rule, token_file}, skipped}` in the usual script envelope, errors first. Waivers:
   `ui-lint-disable <rule>` (file) and `ui-lint-disable-line`. stdlib only, checked against the
   Python 3.6 grammar (the Dell runs 32-bit Debian/antiX).
4. **Hook** (`uilint.go`): the immediate tier only, per file, after `write`/`edit` succeeded:
   at most 12 lines appended to the tool result; clean = nothing; same findings = one line;
   fixed = "is clean now". `--ui-lint on|off`, `JAZZ_UI_LINT`, config `ui_lint`, default on. The
   Stop-time deep pass is not ported: the quality gate and `--review` already run at the end of the
   turn, and the model can call `script ui_lint` on every changed file itself.

## What we leave out, and why

- The **launcher and context system** (`impeccable context`, PRODUCT.md, DESIGN.md sidecars,
  surface briefs, `init`/`document`/`extract`, doctor, pins): a self-contained binary that would
  have to be downloaded and run; jazz ships one static binary and runs on a 32-bit Pentium M.
- The **24 sub-command playbooks** (`critique`, `live`, `generate`, `overdrive`… up to 62 KB each):
  far over the skill budget and written for strong models with browsers; weak free models need a
  short floor, not a design studio.
- **Rendered checks** (contrast on computed colours, overflow, line length, occlusion, cramped
  padding, content hidden at rest, heading rhythm, stragglers): they need a browser; the skill tells
  the model to render when a renderer exists.
- **Taste rules with brand names** (overused fonts list, italic serif display, cream palette): the
  body stays generic as asked, and font-name lists age fast and false-positive on chosen brands.
- **Structural heuristics** that need a DOM (nested cards, icon-tile above heading, same-size card
  grids, hero metrics): kept as prose bans in the skill, not as lint rules, because a regex over
  source cannot decide them without many false positives.
- The **loaded-symbols rule** and the native iOS/Android references: out of scope for a terminal
  agent's web output (the native references are themselves MIT-derived from another project).

## License and attribution

Impeccable is Apache-2.0 (Copyright 2025 Paul Bakaus). The skill body is a rewrite in our own words;
the detector is a reimplementation; the marketing-phrase list is partly reused. Because derived
material ships in the binary, we keep a copy of the license at `third_party/impeccable/LICENSE`, a
`NOTICE` stating what was derived and that it was modified, a header in `ui_lint.py`, and an entry in
`CREDITS.md` (Apache-2.0 §4: license copy, notice of modification, attribution).
