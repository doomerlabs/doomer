#!/usr/bin/env python3
"""Build offline HTML recordings and Depot summaries, including missing reports."""
import html
import json
import os
from pathlib import Path
import sys


def read_result(root, harness, scenario, relative):
    path = root / relative / "report.json"
    try:
        report = json.loads(path.read_text())
        passed = report.get("passed") is True
        detail = report.get("error", "")
    except (OSError, ValueError):
        report = {}
        passed = False
        detail = "No valid report was produced. Inspect this scenario's CI log."
    job_file = root / relative / "ci-job-url.txt"
    job_url = job_file.read_text().strip() if job_file.is_file() else ""
    if not job_url.startswith("https://depot.dev/"):
        job_url = ""
    replay = relative / "report.json.html"
    return {
        "harness": harness,
        "scenario": scenario,
        "passed": passed,
        "selected": True,
        "detail": detail,
        "job_url": job_url,
        "replay": replay.as_posix() if (root / replay).is_file() else "",
        "cached": (report.get("cache") or {}).get("hit") is True,
        "origin": (report.get("cache") or {}).get("origin_environment", ""),
        "cost": 0 if (report.get("cache") or {}).get("hit") else (report.get("usage") or {}).get("estimated_cost_usd", 0),
    }


def render(root, results):
    rows = []
    summary = ["## Agent lifecycle E2E", ""]
    job_url = os.environ.get("DEPOT_JOB_URL", "")
    if job_url:
        summary += [f"[Depot job with HTML recordings and results]({job_url})", ""]
    artifact = os.environ.get("E2E_ARTIFACT_NAME", "agent-e2e-all-recordings")
    summary += [f"Saved artifact `{artifact}`. Download it and open `index.html` to browse the conversations.", "",
                "Each recording link opens its scenario’s Depot job with the downloadable HTML artifact.", "",
                "| Agent | Scenario | Result | Recording |", "| --- | --- | --- | --- |"]
    for result in results:
        status = ("Cached (" + result["origin"] + ")" if result["cached"] else "Passed") if result["passed"] else "Failed"
        if not result.get("selected", True):
            status = "Not selected"
        link = "Unavailable"
        if result["replay"]:
            link = f'<a href="{html.escape(result["replay"], quote=True)}">Conversation</a>'
        rows.append("<tr>" + "".join(f"<td>{html.escape(str(result[key]))}</td>" for key in ("harness", "scenario"))
                    + f'<td>{status}</td><td>{link}</td><td>{html.escape(result["detail"])}</td></tr>')
        recording = f'[HTML artifact]({result["job_url"]})' if result["job_url"] else "See combined gallery"
        summary.append(f'| {result["harness"]} | {result["scenario"]} | {status} | {recording} |')
    selected = [result for result in results if result.get("selected", True)]
    passed = sum(result["passed"] for result in selected)
    summary += ["", f"{sum(result['cached'] for result in selected)} cached; {sum(not result['cached'] for result in selected)} executed or missing.",
                f"{len(results) - len(selected)} not selected."]
    billing = "Codex uses API billing. Claude uses subscription allowance, with no Anthropic API fallback."
    cost = sum(result["cost"] for result in results if result["harness"] == "codex")
    summary += ["", f"{passed}/{len(selected)} selected scenarios passed.", "", f"Estimated Codex model cost is ${cost:.4f}.", "", billing]
    root.mkdir(parents=True, exist_ok=True)
    (root / "index.html").write_text(
        '<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width">'
        '<title>Agent E2E recordings</title>'
        '<style>body{font:16px system-ui;max-width:1200px;margin:3rem auto;padding:1rem}'
        'table{width:100%;border-collapse:collapse}td,th{padding:.65rem;text-align:left;border-bottom:1px solid #ddd}</style>'
        f'<h1>Agent E2E recordings</h1><p>{passed}/{len(selected)} selected scenarios passed. '
        f'{len(results) - len(selected)} not selected.</p><p>{billing}</p>'
        '<table><tr><th>Agent</th><th>Scenario</th><th>Result</th><th>Recording</th><th>Details</th></tr>'
        + "".join(rows) + '</table>'
    )
    summary_path = os.environ.get("GITHUB_STEP_SUMMARY")
    if summary_path:
        with open(summary_path, "a") as output:
            output.write("\n".join(summary) + "\n")


def main():
    mode, directory = sys.argv[1:3]
    root = Path(directory)
    if mode == "--single":
        harness, scenario = sys.argv[3:5]
        results = [read_result(root, harness, scenario, Path("."))]
    elif mode == "--all":
        scenarios = json.loads(os.environ["E2E_SCENARIOS"])
        results = [read_result(root, harness, scenario, Path(f"agent-e2e-{harness}-{scenario}"))
                   for harness in ("codex", "claude") for scenario in scenarios]
        selection = os.environ.get("E2E_SELECTED_TESTS")
        if selection is not None:
            selected = json.loads(selection)
            catalog = {result["harness"] + "/" + result["scenario"] for result in results}
            if (not isinstance(selected, list) or any(not isinstance(test, str) for test in selected)
                    or len(set(selected)) != len(selected) or not set(selected) <= catalog):
                raise ValueError("invalid selected E2E tests")
            for result in results:
                if result["harness"] + "/" + result["scenario"] not in selected:
                    result.update(selected=False, passed=False, cached=False, cost=0, replay="", job_url="",
                                  detail="Jev did not select this scenario for this change.")
    else:
        raise ValueError("expected --single or --all")
    render(root, results)
    if mode == "--all" and (not results or not all(result["passed"] for result in results if result.get("selected", True))):
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
