# "Is Grep All You Need?" and what it means for jazz-cli

**Verified:** arXiv:2605.15184, *"Is Grep All You Need? How Agent Harnesses Reshape Agentic Search"*, by Sen, Kasturi, Lumer, Gulati and Subbiah (PwC). It is a 9-page cs.CL paper submitted 2026-05-14 under CC BY 4.0. **No code repo is released.** The paper has no GitHub link, and a web search turned up only a DAIR.AI summary. That summary is wrong: it says the paper is about *coding agents*, and it is not.

## 1. What the paper actually is

- **Task, not coding:** 116 LongMemEval-S questions over long multi-session chat histories (§3.1), on a corpus of chat turns plus temporal "events" pre-extracted by their Chronos pipeline (§3.2.1).
- **Retrievers:** in-process regex grep scored by match count (§3.2.2) vs ANN vector search with a reranker (§3.2.3).
- **Harnesses:** **Chronos** (their LangChain harness, with category-conditioned dynamic prompting and a pre-loaded "top-15 vector results" block, §3.3.1) and **Claude Code, Codex, Gemini CLI**, which get "a dynamically generated search strategy" and wrapper scripts (§3.3.2).
- **Delivery:** inline tool results, or "programmatic" results written to a file the agent must read (§3.4).
- **Models:** Opus 4.6, Haiku 4.5, GPT-5.4, Gemini 3.1 Pro, Flash-Lite (§3.5). The weak ones are Haiku and Flash-Lite; **no open or free models**. GPT-4o grades (§3.6).
- **Absent:** file/symbol pre-loading, Plan/Execute/Verify/Recover, evaluated intent routing, any coding benchmark, and any cost, latency or token numbers.

## 2. Results (with numbers)

**Table 1 (Exp. 1, full haystack, accuracy %)**

| Model / harness | grep inline | vector inline | grep file | vector file |
|---|---|---|---|---|
| Opus / Chronos | **93.1** | 83.6 | 80.2 | 81.9 |
| Opus / Claude Code | 76.7 | 75.0 | 68.1 | 79.3 |
| Haiku / Chronos | 83.6 | 76.7 | 83.6 | 81.9 |
| Haiku / Claude Code | 55.2 | 44.0 | 37.1 | 32.8 |
| GPT-5.4 / Chronos | 89.7 | 81.9 | 87.1 | 75.0 |
| GPT-5.4 / Codex | 93.1 | 75.9 | **55.2** | 67.2 |
| Gemini Pro / Chronos | 91.4 | 82.8 | 79.3 | 76.7 |
| Gemini Pro / Gemini CLI | 81.9 | 75.0 | 81.0 | 82.8 |
| Flash-Lite / Chronos | 86.2 | 62.9 | 85.3 | 72.4 |
| Flash-Lite / Gemini CLI | 87.1 | 67.2 | 68.1 | 74.1 |

What holds up:

1. **Inline grep beats inline vector in all 10 harness-model pairs**, by +1.7 to +23.3 points. The biggest gaps are on the weak models: Flash-Lite +23.3, Haiku/Claude Code +11.2 (§4.1.3).
2. **The harness matters as much as the retriever.** The same Opus scores 93.1 under Chronos and 76.7 under Claude Code. The same Haiku scores 83.6 and 55.2 (§4.1.4).
3. **File delivery often hurts:** Codex grep 93.1→55.2, Haiku/Claude Code 55.2→37.1, and vector overtakes grep in 5 of 10 pairs. It "trades context bandwidth for compositional tool competence" (§4.1.4).
4. **Added noise does little damage** (Exp. 2, Tables 2–3). At small corpus sizes vector is often *better* (Chronos at s5: grep 83.2–89.7 vs vector 87.9–94.0). Which retriever wins depends on the harness and model. The authors' own hypothesis is that weak models are worse at refining queries, which hurts dense retrieval more (§4.1.4).

**Where the claims outrun the evidence:**

- **n=116, one run.** One question is 0.86 points and the 95% CI is about ±6–7 points. No variance or significance tests.
- **Confound:** the "grep-only" Chronos arm also gets the top-15 *vector* pre-load (§3.3.1). Neither that nor the dynamic prompting is ablated.
- **The tables contradict each other.** The "full" column of Table 2 should match Table 1's inline grep:
  - Claude Code/Opus: 94.0 vs 76.7
  - Chronos/GPT-5.4: 78.5 vs 89.7
  - Claude Code/Haiku: 81.9 vs 55.2
- **Other gaps:** Table 4 is called "programmatic" in the text and "inline" in its caption. Codex rows are missing or "pending" in Exp. 2. The conclusion's "consistently" is contradicted by Exp. 2. §5 itself warns that "code semantics" may behave differently.

**Bottom line:** the paper is a small empirical note on chat memory, not evidence that "grep-first pre-loading makes weak coding agents better". Three lessons do transfer:

- (a) lexical search is strong when answers contain literal strings, as identifiers and tracebacks do;
- (b) return results inline instead of making a weak model open a file;
- (c) harness framing is first-order, so measure it.

## 3. jazz-cli today vs the paper

jazz already matches the paper's best arm: agent-driven `rg` with **inline** results (30 KB, 20% head / 80% tail, full output spilled; `truncate.go`). The system prompt is frozen per session (`buildSystemPrompt`, `prompt.go`). Per-turn context goes only into the **user message** (`pendingNote`, `acTurnInput`, plan reminder in `Agent.Run`, `agent.go` ~l.320–330) or tool results (steering `fileMatch`). That append-only slot is where anything new belongs.

## 4. Mechanisms ranked by expected gain for free models on the Pentium M

### #1. Size bash/rg output to the window, and truncate rg by file (high gain, low risk)
**Evidence:** inline delivery wins, and context pressure hurts weak models (§2.3.1, Table 1).

**Problem:** 30 KB is ~8–10k tokens; on a 32k-window free model two greps fill a third of the context. And the tail-heavy cut (meant for errors) is wrong for `rg`, whose first matches matter most.

**Spec**
- `truncate.go`: add `toolBudget(win int) int`, equal to `clamp(0.08·W·3.5 bytes, 6KB, 30KB)`.
- `tools.go` bash handler: if the first word is `rg`/`grep`/`git grep`, use `truncateGrep` (matches in order, ≤5 per file, then `[+N more in M files; full: <spill>]`). Spill stays a fallback, never the main channel (§4.1.4).
- **Cache:** none (append-only). **Flag:** `JAZZ_GREP_TRUNC=headtail|grouped`, `--tool-budget`.
- **Measure:** input tokens and compactions per task, pass rate. Biggest on SWE-bench SymPy (large repo).

### #2. Lexical pre-fetch at turn start (medium gain, unproven: the paper never tests it)
**Rationale:** each free-model step on the Pentium M costs ~5–20 s plus 429 risk. When the message already names identifiers, pre-computing the first 1–3 "find it" steps saves them.

**Spec:** new `prefetch.go` with `acPrefetch(ctx, root, input string, budgetTok int) string`, called in `Agent.Run` next to `acTurnInput`. It only runs when `!a.sub` and on the **first user turn of a task** (or when `/goal` is set).

1. **Pick candidates from the input:**
   backticked spans, `CamelCase`/`snake_case` names of 4+ characters, dotted paths, file paths, traceback frames (`File "x.py", line N`, `x.go:12`) and quoted error strings. Drop stoplist words; keep at most 6.
2. **Run one search:** `rg -n -F --max-count 3 --max-columns 160 --max-filesize 1M -e c1 -e c2…`, plus one `fd` call for path-like tokens. The hard timeout is 3 s; on timeout, nothing is injected.
3. **Drop vague terms:** any term with more than 40 hits is removed. This is the paper's precision point: grep pays off only when the pattern is distinctive.
4. **Output format:** `<system-reminder>Auto-grep (may be incomplete, verify before editing):\npath:line: snippet…</system-reminder>`, prepended to `input`.
   - Budget: **≤1,000 tokens** (about 3.5 KB), at most 12 hits, at most 3 per file.
   - **No file bodies**, to avoid bloat and stale content.
5. If there are 0 hits, inject nothing.

**Cache:** user message only, never the system prompt; not re-run on later turns; gone after compaction, which is fine.

**Risks:** distractor hits can anchor a weak model on the wrong file; it adds 0.5–3 s of `rg` on the Pentium M; it wastes tokens when the task names no identifiers (terminal tasks).

**Flag:** `--prefetch=off|grep`, config `"prefetch"`, env `JAZZ_PREFETCH`. Default it to **off** until the A/B wins.

**Measure:** steps to the first relevant `read`, total steps and wall time (primary); pass rate (secondary); prefetch precision, the share of injected files later read or edited per the session JSONL (diagnostic).

**Expected:** biggest effect on the 20 SymPy instances, whose issues name functions. Small effect on Aider polyglot, which already names the file. About zero on the terminal tasks.

### #3. One more search-refinement rule in `basePrompt` (low cost, small gain)
**Evidence:** grep "punishes vocabulary mismatch" (§4.1.4), and weak models refine queries badly.

**Add one line:** "Search exact identifiers/error text with `rg -n -F`; if 0 hits, shorten the substring, add `-i`, or search the file name with fd; don't conclude something is absent from one failed search."

**Cache:** this is a per-version change, so it is still byte-stable within a session.

**A/B:** `--system-append` with the line vs without, on the hard suite.

### #4. Per-turn intent hint, the analogue of Chronos's category routing (uncertain, can hurt)
Confounded in the paper. If tried, a regex classifier (traceback / failing test / feature / question) adds one reminder of at most 60 tokens, e.g. traceback → "reproduce first, then open the listed frames". **Never in the system prompt.**

**Risk:** a wrong route misleads weak models. **Flag:** `JAZZ_ROUTE`. Try it last.

### Do not adopt
- **File-based results as the main channel:** Table 1's sharpest regression (−38 points Codex, −18 Haiku/Claude Code).
- **Vector/embedding retrieval:** no shown gain for code; needs an index plus embedding API or local model (infeasible on 2 GB); §5 hedges.
- **Whole files or a repo map in the system prompt:** breaks the frozen-prompt cache and bloats small windows.
- **Extra Plan/Execute/Verify/Recover stages:** not in the paper; `--until`/`--review`/verify nudge already cover it; more calls mean more 429s and latency.

## 5. A/B protocol
- **Pin the model** (`omni.enabled=false`, or log the served model and drop runs that fell back). The paper's main lesson is that model×harness interaction swamps everything else.
- **Paired arms:** same task and commit, ≥3 reps per arm on the 10-task and hard suites, 1–2 on SWE-bench SymPy (one task = 5 points).
- **Report:** pass rate (McNemar/sign test), median steps, wall time, input tokens, compactions, 429s, prefetch precision. A change under 2 of 20 tasks is noise; decide on steps and time first.

*Sources: arxiv.org/abs/2605.15184 (§§2–5, Tables 1–4); the DAIR.AI summary at academy.dair.ai/papers/is-grep-all-you-need, which wrongly calls it a coding-agent paper; jazz-cli `DESIGN.md`, `prompt.go`, `agent.go`, `truncate.go` (read only).*
