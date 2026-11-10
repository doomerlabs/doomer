#!/usr/bin/env python3
"""Verify all required shared results, retaining artifacts for every cache hit."""
import argparse
import concurrent.futures
import json
import os
from pathlib import Path
import shutil
import subprocess
import time


def read_report(path):
    try:
        data = json.loads(path.read_text())
        return data if isinstance(data, dict) else {}
    except (OSError, ValueError):
        return {}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--output', required=True)
    parser.add_argument('--populate', action='store_true')
    parser.add_argument('--jobs', type=int, default=4)
    args = parser.parse_args()
    if args.jobs < 1:
        parser.error("--jobs must be positive")
    harness_workers = min(2, args.jobs)
    root = Path(args.output).resolve()
    root.mkdir(parents=True, exist_ok=True)
    publish = root / 'verified'
    publish.mkdir(parents=True, exist_ok=True)
    start = time.monotonic()
    scenarios = subprocess.check_output(['go', 'run', './tests/codexinstall', '--scenario', 'list'], text=True).split()
    selected = os.environ.get('E2E_SUITE_SCENARIOS')
    if selected:
        requested = selected.split(',')
        if len(set(requested)) != len(requested) or any(name not in scenarios for name in requested):
            raise SystemExit('Invalid scenario subset')
        scenarios = requested
    if not scenarios:
        raise SystemExit('No scenarios selected')

    def suite(harness):
        names = [s for s in scenarios if not (harness == 'claude' and s == 'browser-onboarding')]
        directory = root / ('suite-' + harness)
        env = dict(os.environ, E2E_REPORT_DIR=str(directory), E2E_SUITE_SCENARIOS=','.join(names),
                   E2E_CACHE_ONLY='0' if args.populate else '1', E2E_JOBS=str(max(1,args.jobs//harness_workers) if args.populate else args.jobs))
        # Normal local population follows CI's one fresh-home retry for failures.
        subprocess.run(['scripts/ci-agent-e2e.sh', harness, 'all'], env=env, check=False)
        results = {}
        for scenario in names:
            target = publish / f'agent-e2e-{harness}-{scenario}'
            source = directory / 'report-runs' / scenario
            report = source / 'report.json'
            passed = False
            if report.is_file():
                data = read_report(report)
                passed = data.get('passed') is True and (args.populate or (data.get('cache') or {}).get('hit') is True)
            if args.populate and not passed:
                retry = root / 'retries' / harness / scenario
                retry_env = dict(env, E2E_REPORT_DIR=str(retry))
                subprocess.run(['scripts/ci-agent-e2e.sh', harness, scenario], env=retry_env, check=False)
                source, report = retry, retry / 'report.json'
                if report.is_file():
                    data = read_report(report)
                    passed = data.get('passed') is True
            results[harness + '/' + scenario] = passed
            if passed:
                shutil.copytree(source, target, dirs_exist_ok=True)
        return results

    cached = {}
    # Split the worker limit between harnesses during real execution.
    with concurrent.futures.ThreadPoolExecutor(max_workers=harness_workers) as pool:
        for results in pool.map(suite, ('codex', 'claude')):
            cached.update(results)

    baseline = root / 'policy-regression'
    env = dict(os.environ, E2E_REPORT_DIR=str(baseline), E2E_EXPECT_POLICY_REGRESSION='1',
               E2E_CACHE_ONLY='0' if args.populate else '1', E2E_CANDIDATE_SOURCE=os.environ['E2E_BASELINE_SOURCE'])
    result = subprocess.run(['scripts/ci-agent-e2e.sh', 'codex', 'policy-recovery'], env=env, check=False)
    regression = result.returncode == 0
    if regression:
        shutil.copytree(baseline, publish / 'policy-regression', dirs_exist_ok=True)
    expected = len(cached)
    hits = sum(cached.values())
    status = dict(cached=cached, regression=regression, verified=hits, required=expected,
                  complete=hits == expected and regression, duration_seconds=time.monotonic()-start)
    for target in (root / 'cache-plan.json', publish / 'cache-plan.json'):
        target.write_text(json.dumps(status, indent=2)+'\n')
    output = os.environ.get('GITHUB_OUTPUT')
    if output:
        with open(output, 'a') as stream:
            stream.write('cached=' + json.dumps(cached, separators=(',', ':')) + '\n')
            stream.write('regression=' + json.dumps(regression) + '\n')
            stream.write('max_parallel=' + str(80 if status['complete'] else 8) + '\n')
    print(f"Verified {hits}/{expected} scenario results; baseline={regression}; {status['duration_seconds']:.1f}s")
    if args.populate and not status['complete']:
        raise SystemExit('Local population has failures; inspect retained reports')


if __name__ == '__main__':
    main()
