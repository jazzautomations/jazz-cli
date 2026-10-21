---
name: frontend-craft
description: Design craft for frontend work. Use when building or changing a page, screen, component, layout or stylesheet (html, jsx, tsx, vue, svelte, astro, css), or when the user asks for design, UI, UX, a landing page or a dashboard.
---
# Frontend craft
The user's brief wins: honor any pinned palette, font, era or style even when it breaks a rule below.
Pick the mode from the surface being built, not the product:
- Persuade (landing, pricing, campaign): the visitor decides. Commit to one bold idea; the primary action is visible in the first screen.
- Operate (app, dashboard, admin, settings, tool): the visitor works. Standard controls, scanable density, consistency over expression.
- Read (docs, article, help, changelog): the visitor understands. Calm column of 60-75 characters, clear headings, way forward.
- Experience (portfolio, gallery, showcase): the work leads from the first screen; the interface recedes.
Reflexes:
- Hierarchy: one focal point per screen; size and weight steps you can see (at least 1.25x between type roles).
- Spacing: one scale (4px base: 4 8 12 16 24 32 48 64). Tight inside a group, generous between groups, more space above a heading than below.
- Type: body 16px, line-height 1.5-1.7, at most two families, display under 6rem.
- Colour: palette as CSS variables in one place, then only var(--...). Text contrast at least 4.5:1 (3:1 for large text). On a coloured surface tint secondary text from that hue, never plain grey.
- Motion: one purposeful moment, ease-out, no bounce; every animation gets a prefers-reduced-motion path.
- States: hover, visible keyboard focus (:focus-visible), active, disabled, loading, empty and error, each with real copy.
- Responsive: mobile first, no horizontal scroll at 360px, tap targets 44px, images with width/height.
- A11y: semantic tags, html lang, headings in order, alt on every img (alt="" if decorative), labels on inputs, viewport meta without zoom lock.
- Copy: the product's own words. Buttons name their action; errors say what failed and how to recover.
Never (unless the brief asks for it):
- gradient or clipped text; violet/indigo or cyan-on-dark gradients as the default look;
- zero-offset coloured glow shadows; hard offset block shadows; glass blur as decoration;
- thick coloured border-left/right stripes on cards or callouts;
- a grid of same-size icon+title+text cards as the page; cards inside cards;
- a small uppercase label above every heading; 01/02/03 section numbers;
- emoji or unicode glyphs as icons (use one SVG icon set);
- lorem ipsum, "your text here", buzzwords ("cutting-edge", "next level"); the same radius on everything;
- outline: none without a focus style.
Finish the deliverable: every requested section, real content, working links and controls; nothing left as TODO except assets only the user can provide.
Verify by rendering at the shipped sizes (360px and 1280px) when a renderer exists (a headless browser, a dev server screenshot). One look, fix the whole batch, then look again; no screenshot after every tweak. With no renderer, say "not rendered" and check the HTML/CSS by reading it once.
When jazz appends ui_lint findings to an edit result, fix the real ones in your next batch; keep one on purpose only with a one-line reason.
