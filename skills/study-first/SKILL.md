---
name: study-first
description: Study the relevant code and APIs before editing a medium or large task.
---
# Study first
1. Find the implementation and its callers with rg; read the relevant files and project instructions. Never edit code you have not read. Stay within the exploration budget: use narrow searches and read only the relevant ranges.
2. Before the first edit, cite file:line evidence for the current behavior and list assumptions separately. If an API or version is uncertain, use docs (when available), the installed source or grep. Do not invent signatures.
3. Record a short plan using todo, with the success check for each step. The session retains the plan; reuse a relevant cached plan only after checking it against the current code. Combine todo with the next useful tool call.
4. Edit only after this study. Verify the changed behavior and report the command and observed result. If evidence contradicts the plan, revise the assumptions and plan before continuing.
