.PHONY: schema test help

# Update the schema in all documents that embed it.
#
# Sources of truth:
#   slack_search/search.py      SCHEMA_DESCRIPTION  →  doc/slack-search-skill.md  (brief)
#   prompts/schema.sql                              →  prompts/nl_to_sql.md        (DDL)
#   prompts/schema.sql                              →  doc/slack-search-skill.md   (DDL)
schema:
	uv run python scripts/inject_schema.py

test:
	uv run pytest tests/ -q

help:
	@echo "make schema   — propagate schema changes to all documents"
	@echo "make test     — run the test suite"
