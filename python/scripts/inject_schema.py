"""Inject schema content into all documents that embed it.

Sources of truth
  slack_search/search.py  SCHEMA_DESCRIPTION  → doc/slack-search-skill.md  brief block
  prompts/schema.sql                          → prompts/nl_to_sql.md        DDL block
  prompts/schema.sql                          → doc/slack-search-skill.md   DDL block
"""
from __future__ import annotations
import re
import sys
from pathlib import Path

ROOT = Path(__file__).parent.parent


def inject(path: Path, begin_marker: str, end_marker: str, content: str) -> None:
    text = path.read_text()
    pattern = re.compile(
        re.escape(begin_marker) + r".*?" + re.escape(end_marker),
        re.DOTALL,
    )
    replacement = f"{begin_marker}\n{content}\n{end_marker}"
    new_text, n = pattern.subn(replacement, text)
    if n == 0:
        print(f"  WARNING: markers not found in {path}", file=sys.stderr)
        return
    path.write_text(new_text)
    print(f"  updated {path.relative_to(ROOT)}")


def main() -> None:
    # ── Brief schema from search.py ──────────────────────────────────────────
    sys.path.insert(0, str(ROOT))
    from slack_search.search import SCHEMA_DESCRIPTION  # noqa: PLC0415

    brief_block = f"```\n{SCHEMA_DESCRIPTION.strip()}\n```"

    # ── DDL schema from prompts/schema.sql ───────────────────────────────────
    ddl_sql = (ROOT / "prompts" / "schema.sql").read_text().strip()
    ddl_block = f"```sql\n{ddl_sql}\n```"

    print("Injecting schema …")

    inject(
        ROOT / "doc" / "slack-search-skill.md",
        "<!-- SCHEMA_BEGIN -->",
        "<!-- SCHEMA_END -->",
        brief_block,
    )

    inject(
        ROOT / "doc" / "slack-search-skill.md",
        "<!-- SCHEMA_DDL_BEGIN -->",
        "<!-- SCHEMA_DDL_END -->",
        ddl_block,
    )

    inject(
        ROOT / "prompts" / "nl_to_sql.md",
        "<!-- SCHEMA_DDL_BEGIN -->",
        "<!-- SCHEMA_DDL_END -->",
        ddl_block,
    )

    print("Done.")


if __name__ == "__main__":
    main()
