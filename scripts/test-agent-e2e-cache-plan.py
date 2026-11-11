#!/usr/bin/env python3
import importlib.util
import json
import os
from pathlib import Path
import tempfile
import sys
import threading
import time
from types import SimpleNamespace
import unittest
from unittest.mock import patch


sys.dont_write_bytecode = True

def load(name, filename):
    spec = importlib.util.spec_from_file_location(name, Path(__file__).with_name(filename))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


plan = load('cache_plan', 'agent-e2e-cache-plan.py')
reporter = load('reporter', 'ci-agent-e2e-report.py')


class CachePlanTests(unittest.TestCase):
    def test_malformed_selection_stops_before_execution(self):
        for selection in ('', '{', '{}', '["unknown/test"]'):
            with self.subTest(selection=selection), tempfile.TemporaryDirectory() as directory:
                with patch('sys.argv', ['plan', '--output', directory]), patch.dict(os.environ, {'E2E_SELECTED_TESTS': selection}), patch.object(plan.subprocess, 'check_output', return_value='install\n'), patch.object(plan.subprocess, 'run') as run:
                    with self.assertRaisesRegex(SystemExit, 'Invalid selected E2E tests'):
                        plan.main()
                run.assert_not_called()

    def test_selection_runs_only_the_requested_harness(self):
        calls = []
        def run(command, env, **kwargs):
            calls.append(command)
            report = Path(env['E2E_REPORT_DIR']) / 'report-runs/install/report.json'
            report.parent.mkdir(parents=True)
            report.write_text('{"passed": true}')
            return SimpleNamespace(returncode=0)
        with tempfile.TemporaryDirectory() as directory:
            env = {'E2E_SELECTED_TESTS': '["codex/install"]'}
            with patch('sys.argv', ['plan', '--output', directory, '--populate']), patch.dict(os.environ, env, clear=True), patch.object(plan.subprocess, 'check_output', return_value='install\npolicy-recovery\n'), patch.object(plan.subprocess, 'run', side_effect=run):
                plan.main()
            result = json.loads(Path(directory, 'cache-plan.json').read_text())
        self.assertEqual([['scripts/ci-agent-e2e.sh', 'codex', 'all']], calls)
        self.assertTrue(result['complete'])
        self.assertFalse(result['regression_required'])
        self.assertEqual(1, result['required'])

    def test_empty_selection_executes_nothing(self):
        with tempfile.TemporaryDirectory() as directory:
            with patch('sys.argv', ['plan', '--output', directory, '--populate']), patch.dict(os.environ, {'E2E_SELECTED_TESTS': '[]'}, clear=True), patch.object(plan.subprocess, 'check_output', return_value='install\n'), patch.object(plan.subprocess, 'run') as run:
                plan.main()
            result = json.loads(Path(directory, 'cache-plan.json').read_text())
        run.assert_not_called()
        self.assertTrue(result['complete'])
        self.assertEqual(0, result['required'])

    def test_gallery_accepts_unselected_cases_without_claiming_they_passed(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            target = root / 'agent-e2e-codex-install'
            target.mkdir()
            (target / 'report.json').write_text('{"passed": true}')
            env = {'E2E_SCENARIOS': '["install"]', 'E2E_SELECTED_TESTS': '["codex/install"]'}
            with patch('sys.argv', ['report', '--all', directory]), patch.dict(os.environ, env, clear=True):
                self.assertEqual(0, reporter.main())
            page = (root / 'index.html').read_text()
            self.assertIn('Not selected', page)
            self.assertIn('1/1 selected scenarios passed', page)
            with patch('sys.argv', ['report', '--all', directory]), patch.dict(os.environ, {'E2E_SCENARIOS': '["install"]'}, clear=True):
                self.assertEqual(1, reporter.main())

    def test_missing_and_malformed_reports_are_misses(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'report.json'
            self.assertEqual(plan.read_report(path), {})
            for value in ('{truncated', '[]', 'null', '42', '"text"'):
                path.write_text(value)
                self.assertEqual(plan.read_report(path), {})

    def test_one_worker_never_runs_harnesses_concurrently(self):
        active = peak = 0
        lock = threading.Lock()
        def run(*args, **kwargs):
            nonlocal active, peak
            with lock:
                active += 1
                peak = max(peak, active)
            time.sleep(.01)
            with lock:
                active -= 1
            return SimpleNamespace(returncode=1)
        with tempfile.TemporaryDirectory() as directory:
            argv = ['cache-plan', '--output', directory, '--populate', '--jobs', '1']
            with patch('sys.argv', argv), patch.dict(os.environ, {'E2E_BASELINE_SOURCE': directory}), patch.object(plan.subprocess, 'check_output', return_value='install\n'), patch.object(plan.subprocess, 'run', side_effect=run):
                with self.assertRaises(SystemExit):
                    plan.main()
        self.assertEqual(peak, 1)

    def test_gallery_labels_cache_and_only_charges_new_work(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for name, cached in [('cached', True), ('executed', False)]:
                target = root / name
                target.mkdir()
                (target / 'report.json').write_text(json.dumps(dict(passed=True, cache=dict(hit=cached, origin_environment='local'), usage=dict(estimated_cost_usd=.25))))
            results = [reporter.read_result(root, 'codex', 'install', Path(name)) for name in ('cached', 'executed')]
            self.assertEqual(results[0]['cost'], 0)
            self.assertEqual(results[1]['cost'], .25)
            summary = root / 'summary.md'
            with patch.dict(os.environ, {'GITHUB_STEP_SUMMARY': str(summary)}):
                reporter.render(root, results)
            self.assertIn('Cached (local)', (root / 'index.html').read_text())
            self.assertIn('1 cached; 1 executed or missing', summary.read_text())
            self.assertIn('$0.2500', summary.read_text())


if __name__ == '__main__':
    unittest.main()
