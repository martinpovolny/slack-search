"""LLM evaluation framework — runs test cases and judges response quality."""

from __future__ import annotations

import json
import re
import sqlite3
import sys
from dataclasses import dataclass, field
from datetime import datetime
from pathlib import Path
from typing import Any

import pandas as pd
import yaml
from openai import OpenAI

from .ai_query import run_query, QueryResult, SYNTHESISE_MARKER

TESTS_DIR = Path(__file__).parent.parent / "tests"
RESULTS_DIR = TESTS_DIR / "results"
JUDGE_PROMPT_PATH = Path(__file__).parent.parent / "prompts" / "eval_judge.md"


# ── Data types ────────────────────────────────────────────────────────────────

@dataclass
class CheckResult:
    name: str
    passed: bool
    reason: str

    def label(self) -> str:
        return "✓" if self.passed else "✗"


@dataclass
class TestResult:
    test_id: str
    question: str
    query_result: QueryResult
    sql_checks: list[CheckResult] = field(default_factory=list)
    judge_checks: list[CheckResult] = field(default_factory=list)

    @property
    def passed(self) -> bool:
        return all(c.passed for c in self.sql_checks + self.judge_checks)

    @property
    def all_checks(self) -> list[CheckResult]:
        return self.sql_checks + self.judge_checks


# ── SQL rule-based checks ─────────────────────────────────────────────────────

def _run_sql_checks(sql: str | None, checks: list[dict]) -> list[CheckResult]:
    results = []
    for check in checks:
        ctype = check["type"]
        value = check["value"]
        desc = check.get("desc", value)
        ci = check.get("case_insensitive", False)

        haystack = sql or ""
        needle = value
        if ci:
            haystack = haystack.lower()
            needle = needle.lower()

        if ctype == "contains":
            passed = needle in haystack
            reason = f"SQL {'contains' if passed else 'missing'}: {value!r}"
        elif ctype == "not_contains":
            passed = needle not in haystack
            reason = f"SQL {'correctly avoids' if passed else 'wrongly contains'}: {value!r}"
        else:
            passed = False
            reason = f"Unknown check type: {ctype}"

        results.append(CheckResult(name=desc, passed=passed, reason=reason))

    # Always check: does the SQL run without error?
    results.insert(0, CheckResult(
        name="SQL generated",
        passed=sql is not None,
        reason="SQL was generated" if sql else "No SQL found in response",
    ))
    return results


def _check_mode(result: QueryResult, expected_mode: str) -> CheckResult:
    actual = result.mode
    passed = actual == expected_mode
    return CheckResult(
        name=f"mode={expected_mode}",
        passed=passed,
        reason=f"Mode is '{actual}'" + ("" if passed else f", expected '{expected_mode}'"),
    )


# ── LLM judge ────────────────────────────────────────────────────────────────

def _load_judge_prompt() -> str:
    if JUDGE_PROMPT_PATH.exists():
        return JUDGE_PROMPT_PATH.read_text()
    return (
        "Evaluate the AI response against each criterion. "
        "For each: CRITERION: ..., STATUS: PASS|FAIL|WARN, REASON: ..."
    )


def _parse_judge_output(text: str) -> list[CheckResult]:
    results = []
    blocks = re.split(r"\n(?=CRITERION:)", text.strip())
    for block in blocks:
        if not block.startswith("CRITERION:"):
            continue
        m_crit = re.search(r"CRITERION:\s*(.+)", block)
        m_status = re.search(r"STATUS:\s*(PASS|FAIL|WARN)", block)
        m_reason = re.search(r"REASON:\s*(.+)", block)
        if not (m_crit and m_status):
            continue
        name = m_crit.group(1).strip()
        status = m_status.group(1)
        reason = m_reason.group(1).strip() if m_reason else ""
        results.append(CheckResult(
            name=name,
            passed=status == "PASS",
            reason=f"[{status}] {reason}",
        ))
    return results


def judge_answer(
    client: OpenAI,
    model: str,
    question: str,
    sql: str | None,
    nl_answer: str | None,
    criteria: list[str],
) -> list[CheckResult]:
    if not criteria or not nl_answer:
        return []

    criteria_text = "\n".join(f"- {c}" for c in criteria)
    user_content = (
        f"Question: {question}\n\n"
        f"SQL generated:\n```sql\n{sql or '(none)'}\n```\n\n"
        f"Answer:\n{nl_answer}\n\n"
        f"Criteria to evaluate:\n{criteria_text}"
    )

    resp = client.chat.completions.create(
        model=model,
        temperature=0,
        messages=[
            {"role": "system", "content": _load_judge_prompt()},
            {"role": "user", "content": user_content},
        ],
    )
    return _parse_judge_output(resp.choices[0].message.content)


# ── Main eval runner ──────────────────────────────────────────────────────────

def run_eval(
    conn: sqlite3.Connection,
    client: OpenAI,
    model: str,
    test_cases_path: Path | None = None,
    test_ids: list[str] | None = None,
    judge_client: OpenAI | None = None,
    judge_model: str | None = None,
) -> list[TestResult]:
    path = test_cases_path or (TESTS_DIR / "test_cases.yaml")
    cases = yaml.safe_load(path.read_text())

    if test_ids:
        cases = [c for c in cases if c["id"] in test_ids]

    judge_client = judge_client or client
    judge_model = judge_model or model

    results: list[TestResult] = []
    for case in cases:
        print(f"\n{'─'*60}")
        print(f"▶ [{case['id']}] {case['question']}")

        qr = run_query(conn, case["question"], client, model)

        sql_checks = [_check_mode(qr, case.get("expected_mode", "table"))]
        sql_checks += _run_sql_checks(qr.sql, case.get("sql_checks", []))

        if qr.error:
            sql_checks.append(CheckResult("no_error", False, f"Error: {qr.error}"))

        judge_checks = []
        if case.get("judge_criteria") and not qr.error:
            print(f"  Judging answer with {judge_model}…")
            judge_checks = judge_answer(
                judge_client, judge_model,
                case["question"], qr.sql, qr.nl_answer,
                case["judge_criteria"],
            )

        tr = TestResult(case["id"], case["question"], qr, sql_checks, judge_checks)
        results.append(tr)
        _print_result(tr)

    return results


def _print_result(tr: TestResult) -> None:
    overall = "PASS" if tr.passed else "FAIL"
    print(f"\n  Result: {'✅ PASS' if tr.passed else '❌ FAIL'}")
    if tr.query_result.sql:
        print(f"  SQL mode: {tr.query_result.mode}")
    for c in tr.all_checks:
        print(f"    {c.label()} {c.name}: {c.reason}")


def save_results(results: list[TestResult], prompt_path: Path) -> Path:
    RESULTS_DIR.mkdir(parents=True, exist_ok=True)
    ts = datetime.now().strftime("%Y%m%d_%H%M%S")
    out_path = RESULTS_DIR / f"eval_{ts}.json"

    data = {
        "timestamp": ts,
        "prompt_file": str(prompt_path),
        "prompt_hash": _hash_file(prompt_path),
        "summary": {
            "total": len(results),
            "passed": sum(1 for r in results if r.passed),
            "failed": sum(1 for r in results if not r.passed),
        },
        "tests": [
            {
                "id": r.test_id,
                "question": r.question,
                "passed": r.passed,
                "mode": r.query_result.mode,
                "sql": r.query_result.sql,
                "nl_answer": r.query_result.nl_answer,
                "error": r.query_result.error,
                "checks": [
                    {"name": c.name, "passed": c.passed, "reason": c.reason}
                    for c in r.all_checks
                ],
            }
            for r in results
        ],
    }
    out_path.write_text(json.dumps(data, indent=2))
    return out_path


def _hash_file(path: Path) -> str:
    import hashlib
    return hashlib.md5(path.read_bytes()).hexdigest()[:8] if path.exists() else "missing"


def print_summary(results: list[TestResult]) -> None:
    passed = sum(1 for r in results if r.passed)
    total = len(results)
    print(f"\n{'='*60}")
    print(f"EVAL SUMMARY: {passed}/{total} passed")
    print(f"{'='*60}")
    for r in results:
        icon = "✅" if r.passed else "❌"
        failures = [c for c in r.all_checks if not c.passed]
        fail_str = ""
        if failures:
            fail_str = "  failures: " + ", ".join(c.name for c in failures)
        print(f"  {icon} {r.test_id}{fail_str}")
