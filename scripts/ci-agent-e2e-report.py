#!/usr/bin/env python3
"""Build an offline HTML recording index and a Depot job summary, including failures."""
import html
import json
import os
from pathlib import Path
import sys

reports = Path(sys.argv[1])
harness = sys.argv[2]
artifact_url = os.environ.get("E2E_ARTIFACT_URL", "")
suite_file = reports / "suite.json"
suite = json.loads(suite_file.read_text()) if suite_file.exists() else {"results": []}
results = suite["results"]
rows = []
summary = [f"## {harness.title()} lifecycle E2E", ""]
if artifact_url:
    summary += [f"[Download HTML recordings and results]({artifact_url})", "", "Open `index.html` in the downloaded artifact to browse every recorded conversation.", ""]
summary += ["| Scenario | Result | Recording |", "| --- | --- | --- |"]
for result in results:
    name = result["scenario"]
    filename = Path(result["report"]).name
    replay = filename + ".html"
    status = "Passed" if result["passed"] else "Failed"
    link = f'<a href="{html.escape(replay, quote=True)}">Conversation</a>' if (reports / replay).exists() else "Unavailable"
    rows.append(f"<tr><td>{html.escape(name)}</td><td>{status}</td><td>{link}</td></tr>")
    summary.append(f"| {name} | {status} | `{replay}` |")
if not results:
    summary.append("The runner failed before writing any scenario results. Inspect the job log.")
if suite.get("auth_mode") == "subscription":
    billing = "Claude subscription allowance. No Anthropic API billing or fallback."
else:
    billing = f'Codex API reserved spend: ${suite.get("budget_charged_usd", 0):.4f}.'
summary += ["", billing]
reports.mkdir(parents=True, exist_ok=True)
(reports / "index.html").write_text(
    '<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width">'
    f'<title>{html.escape(harness)} E2E recordings</title>'
    '<style>body{font:16px system-ui;max-width:1000px;margin:3rem auto;padding:1rem}'
    'table{width:100%;border-collapse:collapse}td,th{padding:.65rem;text-align:left;border-bottom:1px solid #ddd}</style>'
    f'<h1>{html.escape(harness.title())} E2E recordings</h1><p>{html.escape(billing)}</p>'
    '<table><tr><th>Scenario</th><th>Result</th><th>Recording</th></tr>' + "".join(rows) + '</table>'
)
if os.environ.get("GITHUB_STEP_SUMMARY"):
    with open(os.environ["GITHUB_STEP_SUMMARY"], "a") as output:
        output.write("\n".join(summary) + "\n")
