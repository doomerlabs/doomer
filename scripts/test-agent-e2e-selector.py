import argparse
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch


sys.dont_write_bytecode = True
spec = importlib.util.spec_from_file_location("selector", Path(__file__).with_name("agent-e2e-selector.py"))
selector = importlib.util.module_from_spec(spec)
spec.loader.exec_module(selector)


class SelectorTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name)
        self.git("init", "-q")
        self.git("config", "user.name", "Test")
        self.git("config", "user.email", "test@example.invalid")
        self.write("tests/codexinstall/scenarios.go", 'package main\nvar scenarioNames = []string{"install", "browser-onboarding"}\n')
        self.write("cmd/root.go", "package cmd\n")
        self.write("main.go", "package main\n")
        self.commit()
        self.base = self.git("rev-parse", "HEAD").strip()

    def git(self, *args):
        env = dict(os.environ, GIT_CONFIG_GLOBAL=os.devnull, GIT_CONFIG_NOSYSTEM="1")
        return subprocess.check_output(["git", "-c", "core.hooksPath=" + os.devnull,
                                        "-c", "maintenance.auto=false", "-c", "gc.auto=0",
                                        "-C", str(self.root), *args],
                                       env=env, stderr=subprocess.DEVNULL).decode()

    def write(self, name, text):
        path = self.root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text)

    def commit(self):
        self.git("add", ".")
        self.git("-c", "commit.gpgsign=false", "commit", "-qm", "fixture")

    def test_catalog_and_new_scenarios(self):
        self.write("tests/codexinstall/scenarios.go", 'package main\nvar scenarioNames = []string{"install", "browser-onboarding", "new-case"}\n')
        self.commit()
        tests, changed, forced, request = selector.build_input(self.root, self.base, "HEAD")
        self.assertEqual(5, len(tests))
        self.assertEqual(["claude/new-case", "codex/new-case"], forced)
        self.assertNotIn("claude/browser-onboarding", tests)
        self.assertIn("new-case", request)

    def test_shared_changes_require_every_test(self):
        self.write("cmd/root.go", "package cmd\nfunc broken() {}\n")
        self.commit()
        tests, _, forced, _ = selector.build_input(self.root, self.base, "HEAD")
        self.assertEqual(set(tests), set(forced))

    def test_private_paths_fail_before_request(self):
        self.write(".env.test", "TOKEN=fixture")
        self.commit()
        with self.assertRaisesRegex(ValueError, "outside selector"):
            selector.build_input(self.root, self.base, "HEAD")
        self.assertFalse(selector.allowed_path(".aws/config.md"))

    def test_invalid_and_oversized_catalog_inputs(self):
        with self.assertRaises(ValueError):
            selector.catalog('var scenarioNames = []string{"duplicate", "duplicate"}')
        self.write("docs/example.md", "some text")
        self.commit()
        with patch.object(selector, "MAX_CONTEXT", 10):
            with self.assertRaisesRegex(ValueError, "context exceeds"):
                selector.build_input(self.root, self.base, "HEAD")

    def test_threshold_preserves_new_tests_and_falls_back(self):
        tests = ["codex/a", "claude/a", "codex/new"]
        result = {"probabilities": {"codex/a": 0.5, "claude/a": 0.9, "codex/new": 0.01}}
        selected, decision = selector.select(tests, ["codex/new"], result, 0.5)
        self.assertEqual(tests, selected)
        self.assertEqual("probability threshold", decision)
        self.assertEqual(tests, selector.select(tests, [], {"error": "timeout"}, 0.5)[0])

    def test_response_validation(self):
        tests = ["codex/a"]
        def response(name="codex/a", value=0.75):
            return {"answers": [{"type": "predicate", "name": name, "probability": value}]}
        self.assertEqual({"codex/a": 0.75}, selector.validate_probabilities(response(), tests))
        for bad in [response("unknown"), response(value=True), response(value=float("nan")),
                    response(value=1.1), {"answers": []}, {"answers": response()["answers"] * 2}]:
            with self.assertRaises(ValueError):
                selector.validate_probabilities(bad, tests)

    def test_request_uses_only_jev_and_validates_response(self):
        answer = {"model": "typesafe/jev-1.13.0", "answers": [
            {"type": "predicate", "name": "codex/a", "probability": 0.8}]}
        with patch.object(selector.urllib.request, "urlopen") as call:
            call.return_value.__enter__.return_value.read.return_value = json.dumps(answer).encode()
            result = selector.request_probabilities("fixture code", ["codex/a"], "fixture-key")
            body = json.loads(call.call_args.args[0].data)
        self.assertEqual("typesafe/jev", body["model"])
        self.assertEqual("predicate", body["questions"][0]["type"])
        self.assertEqual(0.8, result["probabilities"]["codex/a"])
        self.assertEqual("typesafe/jev-1.13.0", result["resolved_model"])

    def test_missing_key_records_conservative_result(self):
        self.write("docs/example.md", "example")
        self.commit()
        output = self.root / "output"
        args = argparse.Namespace(repo=str(self.root), base=self.base, head="HEAD", output=str(output), threshold=0.5)
        with patch.dict(os.environ, {"E2E_CONCENTRATE_API_KEY": ""}):
            selector.predict(args)
        result = json.loads((output / "prediction.json").read_text())
        self.assertEqual("full-suite fallback", result["decision"])
        self.assertEqual(result["catalog"], result["selected"])

    def test_unsupported_base_catalog_uses_current_full_catalog(self):
        self.write("tests/codexinstall/scenarios.go", "package main\nvar scenarioNames = loadScenarios()\n")
        self.commit()
        unsupported = self.git("rev-parse", "HEAD").strip()
        self.write("tests/codexinstall/scenarios.go", 'package main\nvar scenarioNames = []string{"install"}\n')
        self.commit()
        output = self.root / "output"
        selector.predict(argparse.Namespace(repo=str(self.root), base=unsupported, head="HEAD",
                                            output=str(output), threshold=0.5))
        result = json.loads((output / "prediction.json").read_text())
        self.assertEqual(["codex/install", "claude/install"], result["selected"])
        self.assertEqual("full-suite fallback", result["decision"])
        self.assertIn("catalog", result["error"])

    def test_comparison_reports_unselected_failure_and_missing_evidence(self):
        prediction = self.root / "prediction.json"
        prediction.write_text(json.dumps({"head": "head", "catalog": ["codex/a", "claude/a", "codex/b"], "selected": ["codex/a"]}))
        results = self.root / "results"
        for agent, passed in [("codex", True), ("claude", False)]:
            path = results / ("agent-e2e-" + agent + "-a")
            path.mkdir(parents=True)
            (path / "report.json").write_text(json.dumps({"harness": agent, "scenario": "a", "passed": passed}))
        output = self.root / "comparison"
        with patch.dict(os.environ, {}, clear=True):
            selector.compare(argparse.Namespace(prediction=str(prediction), results=str(results), output=str(output)))
        report = json.loads((output / "comparison.json").read_text())
        self.assertEqual(["claude/a"], report["failures_outside_selection"])
        self.assertEqual(["codex/b"], report["missing_results"])


if __name__ == "__main__":
    unittest.main()
