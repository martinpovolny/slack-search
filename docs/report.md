# Slack Archive Search — Project Report

## 1. Project Overview

This project is a CLI and web tool for archiving Slack channels locally and querying them with SQL or natural language. It was built with two motivations in mind.

The first motivation is experimental: to learn and practice **AI-assisted software development** (AI-SDLC). The entire project was built through an agentic workflow — using Claude Code as a pair programmer to design, implement, debug, and iterate on the codebase. This includes not just code generation but higher-level tasks: architectural decisions, prompt engineering, building an evaluation framework, and running iterative improvement loops. The project serves as a case study in what modern AI-assisted development looks like in practice.

The second motivation is practical: to have a **smarter way to search Slack messages**. Slack's built-in search is limited — it lacks date arithmetic, aggregations, cross-channel analysis, and the ability to ask open-ended questions like "what did the team discuss last week?" This tool archives messages locally in SQLite and lets users query them with raw SQL or plain English, with an LLM translating natural language into queries and summarising results.

## 2. Architecture

The tool is built in Python 3.11+ and managed with `uv`. It has two interfaces — a CLI and a Streamlit web UI — both backed by the same core modules.

### Components

| Module | Role |
|---|---|
| `cli.py` | Click-based CLI entry point (`download`, `search`, `nlq`, `eval` commands) |
| `curl_parser.py` | Parses a Chrome DevTools "Copy as cURL" command to extract credentials |
| `slack_client.py` | Raw HTTP client for the Slack API (POST form-body auth) |
| `downloader.py` | Channel resolution, paginated message fetch, thread hydration |
| `database.py` | SQLite schema and CRUD operations |
| `search.py` | SQL runner with Rich table output and schema documentation |
| `ai_query.py` | NL→SQL pipeline: phase 1 (generate SQL) and phase 2 (synthesise answer) |
| `conversations_db.py` | Stores chat conversation history for the Streamlit UI |
| `eval.py` | Evaluation framework: rule-based SQL checks + LLM-as-judge |
| `app.py` | Streamlit web UI with conversation sidebar and chat interface |

### Data flow

```
User question
     │
     ▼
 ai_query.py ── phase 1 ──► LLM (NL→SQL)
     │                           │
     │◄──────────── SQL ─────────┘
     │
     ▼
 SQLite DB  ──► query results (DataFrame)
     │
     ▼ (if [SYNTHESISE] mode)
 ai_query.py ── phase 2 ──► LLM (results→natural language answer)
     │
     ▼
 CLI output / Streamlit chat
```

### Key design decisions

**Custom HTTP client instead of the Slack SDK.** Enterprise Slack rejects `xoxc-` browser tokens when sent in the standard `Authorization: Bearer` header. The Slack SDK uses that header. Instead, the tool posts tokens as form-body fields alongside the full browser cookie string — exactly as the browser does — which Enterprise Slack accepts.

**SQLite as the local store.** Messages are stored in a single `~/.slack-search/messages.db` file. This makes the tool self-contained, portable, and trivially queryable with any SQL tool. No server to run, no migrations to manage.

**Incremental sync.** A `download_state` table tracks the newest and oldest timestamps fetched per channel. Re-running the download picks up from where it left off rather than re-fetching the full history.

## 3. Data Collection

### Authentication

Slack has three token types, each with different access patterns:

- **`xoxp-`** (user token) and **`xoxb-`** (bot token) — standard API tokens, sent in the `Authorization: Bearer` header. Work with the official Slack SDK.
- **`xoxc-`** (browser session token) — extracted from an active browser session. Enterprise Slack rejects these when sent as a Bearer token. They must be posted as a form-body field, accompanied by the full browser cookie string (not just the `d=xoxd-…` cookie).

The recommended credential workflow for Enterprise Slack is to open Slack in Chrome, open DevTools, find any `api/conversations.history` network request, and use "Copy as cURL" to capture both the token and the full cookie header. The tool parses this curl command automatically. The `--channel` value must be a Slack channel ID (the `C…` format) — channel names cannot be resolved on Enterprise Slack. The channel ID is visible in the DevTools request URL or payload.

```bash
uv run slack-search download --curl "$(cat .curl)" --channel C04476G1F7H --since "3 weeks ago"
```

### Channel resolution

`conversations.list` is banned on Enterprise Slack for most tokens, making channel name resolution unreliable. In practice, **a channel ID must be provided** on the first download (e.g. `--channel C04476G1F7H`). The ID is then cached in the database under the channel name, so on subsequent runs you can pass the name instead (`--channel cost-mgmt-dev`) and the tool will resolve it locally without any API call.

### Pagination and threads

Messages are fetched in pages using the Slack `conversations.history` API with cursor-based pagination. For each top-level message that has replies, `conversations.replies` is called to fetch the full thread. File attachments are optionally downloaded to a local directory.

### Rate limiting

A hard cap of one request per second is enforced in `SlackClient._throttle()`. On HTTP 429 responses the client backs off using the `Retry-After` header value.

## 4. NL→SQL Pipeline

The core of the tool is a two-phase pipeline that translates a natural language question into a SQL query, executes it, and optionally produces a natural-language answer from the results.

### Phase 1: NL → SQL

The user's question is sent to an LLM with a system prompt (`prompts/nl_to_sql.md`) that describes the database schema, SQLite dialect quirks, and useful query patterns. The LLM returns a SQL query wrapped in a code block.

The system prompt also instructs the LLM to decide which **response mode** is appropriate:

- **Table mode** (default) — the SQL is executed and results are displayed as a table. Used for factual lookups and aggregates where the numbers speak for themselves ("how many messages did Martin send?", "who sends the most messages?").
- **Synthesise mode** — the SQL is executed, then the results are passed back to the LLM for a natural-language answer. Triggered by writing `[SYNTHESISE]` at the start of the response. Used when the question requires reading and combining message content ("what did the team discuss this week?", "what was happening last Friday?").

The LLM decides which mode to use based on the nature of the question — no user configuration needed.

### Phase 2: Synthesis

When synthesise mode is triggered, the query results (capped at 100 rows, formatted as a markdown table) are sent to the LLM along with the original question and the SQL query itself. The SQL is included so the LLM can understand what was filtered — for example, which user or date range the results correspond to — without needing to re-derive it from the data alone.

The synthesis system prompt (`prompts/synthesis.md`) includes today's date so the LLM can correctly interpret relative date references like "last Friday" when the SQL has already resolved them to concrete dates.

### Prompts

All prompts are stored as plain Markdown files in the `prompts/` directory:

| File | Purpose |
|---|---|
| `prompts/nl_to_sql.md` | Phase 1 system prompt — schema, dialect rules, query patterns, mode selection |
| `prompts/synthesis.md` | Phase 2 system prompt — how to interpret and summarise query results |
| `prompts/eval_judge.md` | LLM judge prompt — used by the evaluation framework |

## 5. Web UI

The Streamlit web UI (`app.py`) provides a chat-like interface for querying the Slack archive without using the command line.

### Conversation sidebar

Past conversations are listed in the left sidebar, each with an auto-generated title and a unique UUID. The title is produced by asking the LLM to summarise the first question in the conversation — giving each entry a meaningful, human-readable label without any user effort. The UUID is displayed alongside the title and serves as a stable identifier for debugging: when investigating a bad answer it is easy to look up the exact conversation, its full message history, and the SQL that was generated. Clicking a conversation restores the full chat history. A "New conversation" button starts a fresh session.

Conversation history is stored in a **separate SQLite database** (`conversations_db.py`), distinct from the messages archive. The web app opens the messages database read-only — it never writes to it — and manages all UI state (conversations, titles, message history) in its own database. This separation keeps the archive safe from accidental modification and makes the two concerns independently deployable. When the app is launched, the most recent conversation is loaded automatically.

### Chat interface

The main panel is a chat window. The user types a question in natural language; the app runs the NL→SQL pipeline and displays the result inline — either a data table (table mode) or a natural-language answer (synthesise mode). The generated SQL is shown alongside the result for transparency.

### Model selection

The UI exposes a model selector in the sidebar, allowing the user to switch between:
- Local Ollama models
- LM Studio (local or remote)
- RHT models.corp (corporate API)

Settings are persisted per conversation so switching models mid-session is explicit.

## 6. Evaluation Framework

A key part of the AI-SDLC approach is being able to measure quality objectively and iterate on it. The evaluation framework (`slack_search/eval.py`) runs a suite of test cases against the full NL→SQL pipeline and reports pass/fail results.

### Test cases

Test cases are defined in `tests/test_cases.yaml`. The current suite has 9 cases covering two categories:

**Table mode** (simple SQL correctness):
- `count_messages` — basic aggregate
- `top_senders` — GROUP BY + ORDER BY
- `user_messages_count` — user filter + date range + count

**Synthesise mode** (date arithmetic, user lookup, answer quality):
- `user_lookup_name_fields` — user search across all name fields + Monday date offset
- `last_friday` — correct Friday weekday offset, coherent summary
- `weekly_topics` — this-week filter, topic summarisation
- `user_weekly_summary` — user filter + last-7-days, summarised narrative
- `user_recent_activity` — most recent messages from a named user
- `thursday_topics` — Thursday weekday offset, topic extraction

Each case specifies:

- **`question`** — the natural language question to ask
- **`expected_mode`** — whether the answer should be in table or synthesise mode
- **`sql_checks`** — rule-based checks on the generated SQL
- **`judge_criteria`** — quality criteria for the LLM judge to evaluate the natural-language answer

Example test case:

```yaml
- id: last_friday
  question: "What was happening last Friday?"
  expected_mode: synthesise
  sql_checks:
    - {type: contains, value: "+ 2) % 7", desc: "Friday weekday offset"}
    - {type: not_contains, value: "+ 6) % 7", desc: "must NOT use Monday offset for Friday"}
    - {type: contains, value: "timestamp"}
  judge_criteria:
    - "The answer should summarise what happened, not just echo raw message text."
    - "The answer should mention specific topics or activities, not say 'no results'."
```

### Rule-based SQL checks

Each SQL check is evaluated directly against the generated SQL string before any LLM judgement. Check types:

- **`contains`** — the SQL must include this string (optionally case-insensitive)
- **`not_contains`** — the SQL must not include this string

These catch common failure modes deterministically and cheaply: wrong weekday offset, missing JOIN, exact-match name filter instead of LIKE, missing aggregation.

### LLM-as-judge

For synthesise-mode answers, the quality of the natural-language response is evaluated by a second LLM (OpenCode.ai) using the `prompts/eval_judge.md` prompt. The judge is given the original question, the generated SQL, the answer, and a list of criteria. It responds with a structured verdict:

```
CRITERION: The answer should mention specific topics
STATUS: PASS
REASON: The answer lists six distinct discussion topics from the week.

OVERALL: PASS
SUMMARY: The response correctly summarises the team's activity without echoing raw messages.
```

Using a separate, capable model as judge (rather than the same model that generated the answer) gives a more independent assessment.

### Running the eval

```bash
uv run slack-search eval --rht-model llama-3-3-70b-instruct-fp8-dynamic --judge-opencode
```

Individual test cases can be run in isolation for faster iteration:

```bash
uv run slack-search eval --rht-model llama-3-3-70b-instruct-fp8-dynamic --judge-opencode --test-ids last_friday,thursday_topics
```

Results are saved to `tests/results/eval_TIMESTAMP.json` with the prompt hash recorded, making it easy to track which prompt version produced which results.

## 7. Prompt Engineering Iterations

This section is where the AI-SDLC methodology shows its value most clearly. Rather than manually testing queries and hoping for the best, the eval→fix→rerun loop made quality improvements measurable and reproducible. Running the first eval immediately surfaced concrete failure modes that would have been hard to catch through ad-hoc testing alone. Each problem below was diagnosed from eval output, fixed in the prompt, and verified by re-running the relevant test cases.

### Problem 1: Wrong weekday date offset

**Symptom.** A query for "what was happening last Friday?" produced SQL that filtered to the wrong date. The model used the Monday offset formula for Friday.

**Root cause.** The system prompt described the general weekday offset formula but did not include worked examples for each day. The model generalised incorrectly.

**Fix.** Added an explicit table of offsets for all seven days to the prompt, plus concrete SQL examples for both Friday (`+ 2) % 7`) and Monday (`+ 6) % 7`). Also added a `not_contains` SQL check to the eval to catch regressions — a Friday query must never contain the Monday offset.

---

### Problem 2: User name matching too narrow

**Symptom.** Queries like "what did Luke talk about?" produced SQL with `WHERE u.real_name = 'Luke'` — an exact match on a single field. This returned no results when the user's `real_name` was "Luke Couzens" or their `name` was `lcouzens`.

**Root cause.** The prompt did not specify how to search for users by name.

**Fix.** Added an explicit rule: always match across all three name fields (`name`, `real_name`, `display_name`) using case-insensitive LIKE, never exact equality. Example added to the prompt:
```sql
WHERE (u.name LIKE '%luke%' OR u.real_name LIKE '%Luke%' OR u.display_name LIKE '%Luke%')
```

---

### Problem 3: Synthesis saying "no results" when there were results

**Symptom.** The phase 2 LLM received query results from last Friday but opened its answer with "Last Friday is not directly mentioned in the provided results, as the results are from June 12, 2026." It did not connect the concrete date to the relative label "last Friday."

**Root cause.** The synthesis prompt had no awareness of the current date. The LLM saw rows dated June 12 but could not verify that June 12 was last Friday.

**Fix.** Injected today's date (e.g. "Saturday, 2026-06-13") into the synthesis system prompt at runtime, and added an explicit instruction not to caveat date labels that the SQL has already resolved.

---

### Problem 4: Synthesis doubting its own user filter

**Symptom.** A query for "what did Luke talk about on Thursday?" produced correct SQL (with a proper LIKE filter and Thursday offset), but the synthesis answer said "the results do not contain any messages from Luke — the messages appear to be from other users." The answer then correctly described Luke's topics, contradicting itself.

**Root cause.** The SQL selected only `text` and `date`, with no `real_name` column in the output. The synthesis LLM could not see who sent each row, so it second-guessed the WHERE clause filter.

**Fix.** Two changes: (1) added a rule to the NL→SQL prompt to always include `u.real_name` in SELECT when filtering by a specific user; (2) added an explicit instruction to the synthesis prompt: "ALL rows satisfy the WHERE clause — if the SQL filters by a user name, every returned row is from that user; do not claim otherwise."

## 8. Infrastructure

The tool is designed to work with any OpenAI-compatible API endpoint. During development and evaluation, Red Hat IT-hosted LLM models were used via the corporate API (`models.corp`). These are production-grade models served on Red Hat infrastructure, including Llama 3.3 70B and several Qwen and Granite variants.

The tool also supports local models via LM Studio. During development, `Qwen/Qwen3.6-27B` was run locally on a Mac Studio using LM Studio, consuming approximately 18 GB of RAM. While this provides a fully offline, zero-cost alternative, token generation was noticeably slow compared to API-hosted models — making it better suited for occasional use than interactive querying. Any OpenAI-compatible endpoint can be configured via the `LLM_BASE_URL` and `LLM_MODEL` environment variables.

One practical challenge during development was network reliability: connectivity to the corporate API depends on VPN access, and intermittent VPN drops caused some eval runs to fail mid-suite with connection errors rather than prompt failures. The tool detects these and reports them clearly, and the eval can be re-run on individual test cases to recover quickly.

## 9. Results

### Eval progression

The evaluation framework was built and run iteratively. Each run exposed concrete failures that were fixed in the prompts and re-tested. The table below shows the progression:

| Run | Score | Main failures |
|---|---|---|
| Initial (first working run) | 8/9 | `last_friday` — synthesis said "no results" despite correct SQL and real data |
| After synthesis date fix | 8/9 | `thursday_topics` — synthesis doubted its own user filter |
| After WHERE clause trust fix | **9/9** | All passing |

### Final results (9/9 passing)

```
✅ count_messages
✅ top_senders
✅ user_lookup_name_fields
✅ last_friday
✅ weekly_topics
✅ user_weekly_summary
✅ user_messages_count
✅ user_recent_activity
✅ thursday_topics
```

### Observations

The rule-based SQL checks (weekday offsets, LIKE vs `=`, required clauses) proved highly reliable — they caught all SQL-level regressions immediately and deterministically. The LLM judge was more valuable for catching answer-quality issues that are impossible to express as string matching: whether the response summarised content vs. echoed raw messages, whether it incorrectly claimed there were no results, and whether it correctly attributed messages to the right person.

The most common failure pattern across all runs was the synthesis LLM being overly cautious — hedging with "no results found" or "data may not contain this information" even when the SQL had returned correct, relevant data. Each instance was fixed by providing the model with more context (today's date, explicit trust in the WHERE clause) rather than by changing the SQL generation logic.
