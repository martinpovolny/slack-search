Analyze a conversation from the Slack Search web UI by its conversation ID.

Arguments: $ARGUMENTS
Extract the UUID from the arguments (the format is `id: <uuid>` or just the UUID itself).

## Step 1 — Fetch the conversation

Run this Python script to load the conversation from the database:

```python
import sqlite3, sys

CONV_DB = "/Users/martinpovolny/.slack-search/conversations.db"
cid = "<CONVERSATION_ID>"  # replace with extracted UUID

conn = sqlite3.connect(CONV_DB)
conn.row_factory = sqlite3.Row

conv = conn.execute("SELECT * FROM conversations WHERE id=?", (cid,)).fetchone()
if not conv:
    print(f"Conversation {cid} not found")
    sys.exit(1)

print(f"Title:   {conv['title']}")
print(f"Created: {conv['created_at']}")
print()

msgs = conn.execute(
    "SELECT role, content, sql FROM messages WHERE conversation_id=? ORDER BY created_at",
    (cid,)
).fetchall()

for m in msgs:
    print(f"--- {m['role'].upper()} ---")
    print(m['content'])
    if m['sql']:
        print(f"\n[SQL]:\n{m['sql']}")
    print()
```

## Step 2 — Analyze

After fetching the conversation, analyze it for the following issues:

**SQL correctness:**
- Does the SQL use `datetime(...) LIKE '...'` for date filtering? (banned — should use `timestamp >= unixepoch(...)`)
- Does the SQL use exact equality on user name fields? (should use LIKE across name/real_name/display_name)
- Does the SQL use SQLite reserved words as column aliases? (e.g. `AS when`)
- Is the weekday offset formula correct? (use the modulo formula, not `strftime('%w') = 'N'`)
- Did the LLM assume a year that falls outside the archive date range?

**Mode selection:**
- Should the response have used synthesise mode but used table mode instead?
- Questions containing "what happened", "what did X talk about", "summarise", "what topics" always require synthesise mode.

**Answer quality:**
- Did the answer correctly reflect the SQL results?
- Did the synthesis say "no results" when there were results?
- Did the synthesis doubt its own user filter?

**Summary:**
Produce a structured report:
1. What the user asked
2. What SQL was generated (and whether it is correct)
3. What the answer was
4. Any issues found (with severity: 🔴 critical / 🟡 warning / 🟢 ok)
5. What the correct SQL and answer should have been (if issues were found)
