package main

import "testing"

func TestPreflightCommandEvidence(t *testing.T) {
	for _, command := range []string{
		"doomer preflight", "doomer    preflight", "doomer preflight --agent codex", "'/tmp/a b/doomer' preflight",
		"cd repo && doomer preflight", "env FOO=bar doomer preflight",
		"/bin/bash -lc 'doomer preflight --repo .'",
		"doomer --profile 'team' --api-url=https://example.test/api preflight",
	} {
		if !invokesDoomerPreflight(command) {
			t.Errorf("missed invocation %q", command)
		}
	}
	for _, command := range []string{
		"my-doomer preflight", "doomer prepare --agent codex", "cat docs/doomer/preflight.md", "git grep doomer preflight",
		"echo 'doomer preflight'", "# doomer preflight\necho ok",
		"doomer task status preflight", "cat <<'EOF'\ndoomer preflight\nEOF",
	} {
		if invokesDoomerPreflight(command) {
			t.Errorf("mistook mention for invocation %q", command)
		}
	}
}

func TestPreflightJSONEvidence(t *testing.T) {
	for _, text := range []string{
		`{"local_only":true,"account_verified":false,"state":"saved_consent_found"}`,
		"doomer preflight\n{\n  \"state\": \"saved_consent_found\",\n  \"account_verified\": false, \"local_only\": true\n}\n",
	} {
		local, reused := preflightEvidence(text)
		if !local || !reused {
			t.Errorf("missing semantic evidence: %q", text)
		}
	}
	local, reused := preflightEvidence(`{"local_only":true,"account_verified":false,"state":"setup_decision_required"}`)
	if !local || reused {
		t.Fatal("setup decision incorrectly counted as reused consent")
	}
	local, reused = preflightEvidence(`{"local_only":true,"state":"saved_consent_found"}`)
	if local || reused {
		t.Fatal("missing account evidence counted as a local preflight")
	}
}
