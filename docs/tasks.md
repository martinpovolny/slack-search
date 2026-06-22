# Tasks

## Presentation v2 — next version ideas

Ideas from the 2026-06-22 critique of `docs/presentation-2026-06-21/`.
Carry forward to a future revision rather than patching the current deck.

- [ ] **Data privacy acknowledgment** — add a slide or callout noting that messages are downloaded to a local SQLite file; acknowledge GDPR / data-retention policy implications and emphasise "all data stays local"
- [ ] **Introduce the web UI earlier** — the synthesise-mode screenshot reveals a web interface that hasn't been mentioned before; add one line to the Query Modes slide noting a Streamlit web UI also exists
- [ ] **NLQ failure modes** — briefly address what happens when the LLM generates invalid SQL (the tool handles it gracefully — worth showing)
- [ ] **Archive scope** — clarify what the archive does and doesn't capture: thread replies, edited messages, file attachments
- [ ] **Map synthesise mode to the opening problem examples** — "Summarise what happened in #incident-response last week" on the Problem slide directly requires synthesise mode; make that connection explicit in the Query Modes or NLQ slides
- [ ] **Synthesise mode prominence** — the `[SYNTHESISE]` prefix mechanism (LLM autonomously decides whether to summarise) is the most impressive design decision; currently buried as a sub-bullet; deserves a dedicated callout or its own slide
