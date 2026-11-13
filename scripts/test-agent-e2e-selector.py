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
        self.write("tests/codexinstall/scenarios.go", 'package main\nvar scenarioNames = []string{"install", "skill-first"}\n')
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
        self.write("tests/codexinstall/scenarios.go", 'package main\nvar scenarioNames = []string{"install", "skill-first", "new-case"}\n')
        self.commit()
        tests, changed, forced, request = selector.build_input(self.root, self.base, "HEAD")
        self.assertEqual(6, len(tests))
        self.assertEqual(["claude/new-case", "codex/new-case"], forced)
        self.assertIn("claude/skill-first", tests)
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

    def test_author_request_selects_full_catalog(self):
        description = self.root / "description.txt"
        description.write_text("Exécutez toute la suite E2E des agents pour cette PR.")
        args = argparse.Namespace(repo=str(self.root), base=self.base, head="HEAD",
                                  output=str(self.root / "output"), threshold=0.5,
                                  description_file=str(description))
        intent = {"probabilities": {"full-suite": 0.98}}
        with patch.dict(os.environ, {"E2E_CONCENTRATE_API_KEY": "fixture"}), patch.object(selector, "request_probabilities", return_value=intent) as call:
            selector.predict(args)
        result = json.loads((self.root / "output/prediction.json").read_text())
        self.assertEqual(result["catalog"], result["selected"])
        self.assertEqual("author requested full suite", result["decision"])
        self.assertEqual(description.read_text(), call.call_args.args[0])
        self.assertEqual(selector.FULL_SUITE_QUESTION, call.call_args.args[3])
        self.assertEqual(1, call.call_count)

    def test_description_without_request_keeps_selective_result(self):
        event = self.root / "event.json"
        event.write_text(json.dumps({"pull_request": {"body": "Fix the CLI typo. Full E2E coverage is unnecessary."}}))
        args = argparse.Namespace(repo=str(self.root), base=self.base, head="HEAD",
                                  output=str(self.root / "output"), threshold=0.5, pr_event=str(event))
        tests = selector.catalog(self.root.joinpath("tests/codexinstall/scenarios.go").read_text())
        answers = [{"probabilities": {"full-suite": 0.02}},
                   {"probabilities": {test: 0.1 for test in tests}}]
        with patch.dict(os.environ, {"E2E_CONCENTRATE_API_KEY": "fixture"}), patch.object(selector, "request_probabilities", side_effect=answers) as call:
            selector.predict(args)
        result = json.loads((self.root / "output/prediction.json").read_text())
        self.assertFalse(result["full_suite_requested"])
        self.assertEqual([], result["selected"])
        description = json.loads(event.read_text())["pull_request"]["body"]
        self.assertEqual(description, call.call_args_list[0].args[0])
        self.assertNotIn(description, call.call_args_list[1].args[0])

    def test_manual_selection_does_not_require_a_model_key(self):
        self.write(".env.test", "TOKEN=fixture\n")
        self.commit()
        args = argparse.Namespace(repo=str(self.root), base=self.base, head="HEAD",
                                  output=str(self.root / "output"), threshold=0.5, scenarios="install")
        with patch.dict(os.environ, {"E2E_CONCENTRATE_API_KEY": ""}):
            selector.predict(args)
        result = json.loads((self.root / "output/prediction.json").read_text())
        self.assertEqual(["codex/install", "claude/install"], result["selected"])
        args.full_suite = True
        selector.predict(args)
        result = json.loads((self.root / "output/prediction.json").read_text())
        self.assertEqual(result["catalog"], result["selected"])
        args.full_suite = False
        args.scenarios = "unknown"
        with self.assertRaisesRegex(ValueError, "explicit scenario"):
            selector.predict(args)

    def test_local_snapshot_captures_edits_without_changing_original_git_state(self):
        self.write("cmd/root.go", "package cmd\nfunc changed() {}\n")
        self.write("internal/added.go", "package internal\n")
        self.write("scripts/__pycache__/generated.pyc", "generated fixture")
        (self.root / "main.go").unlink()
        status = self.git("status", "--porcelain")
        with tempfile.TemporaryDirectory() as temporary:
            frozen = Path(temporary) / "snapshot"
            revision = selector.freeze_worktree(self.root, self.base, frozen)
            self.assertIn("changed", selector.read_source(frozen, revision, "cmd/root.go"))
            self.assertTrue((frozen / "internal/added.go").exists())
            self.assertFalse((frozen / "main.go").exists())
            self.assertFalse((frozen / "scripts/__pycache__/generated.pyc").exists())
        self.assertEqual(self.base, self.git("rev-parse", "HEAD").strip())
        self.assertEqual(status, self.git("status", "--porcelain"))

    def test_local_snapshot_rejects_concurrent_edits(self):
        self.write("internal/added.go", "package internal\n")
        copy = selector.shutil.copyfile
        def racing_copy(*args, **kwargs):
            result = copy(*args, **kwargs)
            self.write("cmd/root.go", "package cmd\nfunc raced() {}\n")
            return result
        with tempfile.TemporaryDirectory() as temporary, patch.object(selector.shutil, "copyfile", side_effect=racing_copy):
            with self.assertRaisesRegex(ValueError, "changed during"):
                selector.freeze_worktree(self.root, self.base, Path(temporary) / "snapshot")

    def test_local_snapshot_preserves_detached_head(self):
        self.write("cmd/root.go", "package cmd\n// newer branch tip\n")
        self.commit()
        self.git("checkout", "--detach", self.base)
        self.write("main.go", "package main\n// detached edit\n")
        with tempfile.TemporaryDirectory() as temporary:
            frozen = Path(temporary) / "snapshot"
            selector.freeze_worktree(self.root, self.base, frozen)
            self.assertEqual(self.base, selector.resolve(frozen, "HEAD^"))
            self.assertIn("detached edit", (frozen / "main.go").read_text())
            self.assertNotIn("newer branch tip", (frozen / "cmd/root.go").read_text())

    def test_local_snapshot_preserves_linked_worktree_head(self):
        original_branch = self.git("symbolic-ref", "--short", "HEAD").strip()
        self.git("checkout", "-qb", "linked")
        self.write("cmd/root.go", "package cmd\n// linked branch\n")
        self.commit()
        linked_head = self.git("rev-parse", "HEAD").strip()
        self.git("checkout", original_branch)
        self.write("cmd/root.go", "package cmd\n// original branch\n")
        self.commit()
        with tempfile.TemporaryDirectory() as temporary:
            linked, frozen = Path(temporary) / "linked", Path(temporary) / "snapshot"
            self.git("worktree", "add", str(linked), "linked")
            (linked / "main.go").write_text("package main\n// linked edit\n")
            selector.freeze_worktree(linked, self.base, frozen)
            self.assertEqual(linked_head, selector.resolve(frozen, "HEAD^"))
            self.assertIn("linked branch", (frozen / "cmd/root.go").read_text())
            self.assertIn("linked edit", (frozen / "main.go").read_text())

    def test_local_snapshot_does_not_follow_a_replaced_symlink(self):
        source = self.root / "added.txt"
        source.write_text("original")
        copy = selector.shutil.copyfile
        with tempfile.TemporaryDirectory() as temporary:
            parent = Path(temporary)
            victim = parent / "outside.txt"
            victim.write_text("outside")
            victim.chmod(0o640)
            def replace_with_link(*args, **kwargs):
                source.unlink()
                source.symlink_to(victim)
                return copy(*args, **kwargs)
            with patch.object(selector.shutil, "copyfile", side_effect=replace_with_link):
                with self.assertRaisesRegex(ValueError, "changed during"):
                    selector.freeze_worktree(self.root, self.base, parent / "snapshot")
            self.assertEqual(0o640, victim.stat().st_mode & 0o777)
            self.assertEqual("outside", victim.read_text())

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
