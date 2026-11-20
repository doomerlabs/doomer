package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/doomerlabs/doomer/internal/livewatch"
	watching "github.com/doomerlabs/doomer/internal/watch"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
)

func TestDeniedAssessmentRequest(t *testing.T) {
	const exe, key = "/opt/doomer", "watch-key"
	const submit = `'/opt/doomer' watch hypotheses --control watch-key`
	const body = "\n{\"request_id\":\"pending\",\"hypotheses\":[]}\nJSON\n"
	for _, tc := range []struct {
		name, command, want string
	}{
		{"direct", submit + " <<'JSON'" + body, "pending"},
		{"pipe", "cat <<'JSON' | " + submit + body, "pending"},
		{"other executable", "/other/doomer watch hypotheses --control watch-key <<'JSON'" + body, ""},
		{"wrong control key", "'/opt/doomer' watch hypotheses --control other <<'JSON'" + body, ""},
		{"file redirect", submit + " < /tmp/response.json", ""},
		{"quoted command text", "echo \"/opt/doomer watch hypotheses --control watch-key\" <<'JSON'" + body, ""},
		{"compound command", "true; " + submit + " <<'JSON'" + body, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := deniedAssessmentRequest(tc.command, exe, key); got != tc.want {
				t.Fatalf("request ID = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDeniedFindingCommand(t *testing.T) {
	const exe, key = "/opt/doomer", "watch-key"
	for _, tc := range []struct {
		command string
		want    bool
	}{
		{"'/opt/doomer' watch findings ls --control watch-key", true},
		{"'/opt/doomer' watch findings respond --control watch-key <<'JSON'\n[]\nJSON", true},
		{"'/opt/doomer' watch findings ls --control other", false},
		{"'/other/doomer' watch findings ls --control watch-key", false},
		{"echo '/opt/doomer watch findings ls --control watch-key'", false},
	} {
		if got := deniedFindingCommand(tc.command, exe, key); got != tc.want {
			t.Errorf("recognized denial for %q = %v, want %v", tc.command, got, tc.want)
		}
	}
}

type assessmentDenialClient struct {
	stopHandoffClient
	denials     chan map[string]any
	submissions chan struct{}
}

func (c assessmentDenialClient) ReportWorkspaceLifecycle(_ context.Context, _ string, body any) error {
	payload := body.(map[string]any)
	if payload["event"].(map[string]any)["error_code"] == "agent_permission_denied" {
		c.denials <- payload
	}
	return nil
}

func (c assessmentDenialClient) StartWorkspace(context.Context, string, adversarylabs.WorkspaceSubmission) (adversarylabs.WorkspaceReview, error) {
	c.submissions <- struct{}{}
	return adversarylabs.WorkspaceReview{}, fmt.Errorf("unexpected review submission")
}

func TestClaudePermissionDeniedReportsOnlyMatchingPendingAssessment(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("local watcher IPC requires Linux or macOS")
	}
	home, repo := t.TempDir(), newWatchTestRepo(t)
	id := hookIdentity{Namespace: home, Agent: "claude", Session: "fixture"}
	cfg := livewatch.Config{ID: id.Key(), ControlKey: id.Key(), Directory: repo, Agent: id.Agent, RequireHypotheses: true, Request: &adversarylabs.WorkspaceUserRequest{Prompt: "private prompt"}}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	client := assessmentDenialClient{denials: make(chan map[string]any, 2), submissions: make(chan struct{}, 1)}
	done := make(chan error, 1)
	go func() {
		done <- livewatch.Serve(ctx, cfg, client, func(context.Context, string) bool { return true })
	}()
	t.Cleanup(func() { cancel(); <-done })
	for {
		if _, ready := livewatch.Available(ctx, id.Key()); ready {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("watcher did not start")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "sample.txt"), []byte("changed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	pending, err := livewatch.Call(ctx, id.Key(), "checkpoint")
	if err != nil || len(pending.HypothesisRequests) != 1 {
		t.Fatalf("expected one pending assessment: %v, %v", pending, err)
	}
	exe, err := turnExecutable()
	if err != nil {
		t.Fatal(err)
	}
	deny := func(request string) {
		t.Helper()
		command := fmt.Sprintf("cat <<'JSON' | %s watch hypotheses --control %s\n{\"request_id\":%q,\"hypotheses\":[],\"private\":\"shell payload\"}\nJSON\n", watching.Quote(exe), id.Key(), request)
		tool, _ := json.Marshal(map[string]string{"command": command})
		input, _ := json.Marshal(watching.HookInput{Event: "PermissionDenied", Session: id.Session, Tool: "Bash", Input: tool, Reason: "[Undermining Oversight] private classifier detail"})
		cmd := NewRootCommand()
		cmd.SetContext(ctx)
		cmd.SetIn(bytes.NewReader(input))
		if out, err := runAgentWatchHook(cmd, home, id.Agent); err != nil || len(out) != 0 {
			t.Fatalf("denial hook must stay silent: %v, %v", out, err)
		}
	}
	request := pending.HypothesisRequests[0]
	deny("stale-request")
	deny(request.ID)
	deny(request.ID)
	for _, stage := range []string{"agent_assessment", "result_delivery"} {
		select {
		case payload := <-client.denials:
			event := payload["event"].(map[string]any)
			if len(payload) != 2 || payload["review_id"] != request.ReviewID || event["stage"] != stage || event["request_id"] != request.ID || event["reason"] != "undermining_oversight" {
				t.Fatalf("denial should report only review identity and normalized event: %v", payload)
			}
		case <-ctx.Done():
			t.Fatalf("no denial event for %s", stage)
		}
	}
	select {
	case <-client.submissions:
		t.Fatal("denied assessment submitted a review")
	default:
	}
}
