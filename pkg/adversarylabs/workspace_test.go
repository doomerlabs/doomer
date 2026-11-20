package adversarylabs

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/doomerlabs/doomer/pkg/sourcesnapshot"
)

func TestWorkspaceUploadUsesOnlyValidatedBlobRoutes(t *testing.T) {
	data := []byte("source")
	sha := sourcesnapshot.Digest(data)
	puts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/test-pack" && r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("missing auth")
		}
		if handleTestPack(t, w, r, func(_ []byte) { puts++ }) {
			return
		}
		switch r.URL.Path {
		case "/api/v1/repositories":
			json.NewEncoder(w).Encode(map[string]any{"repository_id": "repo", "snapshot_id": "snapshot", "pack_url": "/api/v1/repositories/packs", "missing": []map[string]any{{"sha256": sha, "size": len(data), "upload_url": "/api/v1/repositories/blobs/" + sha}}, "finalize_url": "/api/v1/repositories/repo/snapshots/snapshot/finalize"})
		case "/api/v1/repositories/blobs/" + sha:
			puts++
			w.Write([]byte(`{}`))
		case "/api/v1/repositories/repo/snapshots/snapshot/finalize":
			w.Write([]byte(`{"repository_id":"repo","snapshot_id":"snapshot","complete":true,"missing":[]}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client := NewClientWithBaseURL(ConfigStore{}, server.URL+"/api")
	repo, id, err := client.UploadWorkspaceTree(context.Background(), "test-token", "org/repo", []sourcesnapshot.Entry{{Path: "source.go", Hash: sha, Mode: "100644"}}, map[string][]byte{sha: data})
	if err != nil || repo != "repo" || id != "snapshot" || puts != 1 {
		t.Fatalf("upload failed: %s %s %d %v", repo, id, puts, err)
	}
}
func TestWorkspaceRejectsForeignUploadURL(t *testing.T) {
	data := []byte("source")
	sha := sourcesnapshot.Digest(data)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"repository_id": "repo", "snapshot_id": "snapshot", "missing": []map[string]any{{"sha256": sha, "size": len(data), "upload_url": "https://example.com/steal"}}})
	}))
	defer server.Close()
	client := NewClientWithBaseURL(ConfigStore{}, server.URL+"/api")
	_, _, err := client.UploadWorkspaceTree(context.Background(), "private", "org/repo", []sourcesnapshot.Entry{{Path: "file", Hash: sha, Mode: "100644"}}, map[string][]byte{sha: data})
	if err == nil || !strings.Contains(err.Error(), "unexpected upload") {
		t.Fatal(err)
	}
}

func TestWorkspaceRequestsCompactStatusOnSubmitAndPoll(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("view") != "status" {
			t.Error("workspace client requested full diff instead of compact status")
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("missing authentication")
		}
		if r.URL.Query().Has("results") {
			t.Error("compact status must not request Deep results")
		}
		json.NewEncoder(w).Encode(map[string]any{"id": "review", "status": "running", "early_assessment": map[string]any{"status": "ready", "leads": []map[string]any{{"id": strings.Repeat("a", 64), "path": "file.go", "line": 1, "concern": "Check behavior", "check": "Run a test"}}}})
	}))
	defer server.Close()
	client := NewClientWithBaseURL(ConfigStore{}, server.URL+"/api")
	created, err := client.StartWorkspace(context.Background(), "test-token", WorkspaceSubmission{ReviewID: "review"})
	if err != nil || created.ID != "review" {
		t.Fatalf("submit: %+v %v", created, err)
	}
	polled, err := client.GetWorkspace(context.Background(), "test-token", created.ID)
	if err != nil || polled.Early == nil || len(polled.Early.Leads) != 1 || calls != 2 {
		t.Fatalf("poll: %+v %v (calls %d)", polled, err, calls)
	}
}

func TestWorkspaceFeedbackRequiresRecordedAcknowledgment(t *testing.T) {
	for _, recorded := range []bool{false, true} {
		t.Run(map[bool]string{false: "unconfirmed", true: "recorded"}[recorded], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/reviews/workspace/review/feedback" || r.Header.Get("Authorization") != "Bearer token" {
					t.Error("wrong feedback request")
				}
				var input map[string]any
				if json.NewDecoder(r.Body).Decode(&input) != nil || input["status"] != "fixed" || input["reason"] != "Verified fix" || input["agrees"] != true {
					t.Error("missing disposition")
				}
				_ = json.NewEncoder(w).Encode(map[string]bool{"recorded": recorded})
			}))
			defer server.Close()
			client := NewClientWithBaseURL(ConfigStore{}, server.URL+"/api")
			agrees := true
			err := client.ReportWorkspaceLead(context.Background(), "token", "review", strings.Repeat("a", 64), "fixed", "Verified fix", &agrees)
			if (err == nil) != recorded {
				t.Fatalf("acknowledgment: %v", err)
			}
		})
	}
}

func TestWorkspaceAutoDeepFallsBackOnlyForOldEvidenceServers(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		status     int
		explicit   bool
		calls      int
	}{
		{"old-server", "invalid_evidence_context", 400, false, 2},
		{"invalid-snapshot", "invalid_snapshot", 400, false, 1},
		{"access-denied", "invalid_evidence_context", 403, false, 1},
		{"already-disabled", "invalid_evidence_context", 400, true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var inputs []WorkspaceSubmission
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var input WorkspaceSubmission
				if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
					t.Fatal(err)
				}
				inputs = append(inputs, input)
				if len(inputs) == 1 {
					w.WriteHeader(tc.status)
					json.NewEncoder(w).Encode(map[string]string{"error": tc.code})
					return
				}
				json.NewEncoder(w).Encode(map[string]any{"id": "review", "full_review": map[string]any{"enabled": false}, "early_assessment": map[string]any{"status": "ready"}})
			}))
			defer server.Close()
			disabled := false
			input := WorkspaceSubmission{ReviewID: "same-request", EvidenceContext: true, FullSnapshotID: strings.Repeat("a", 64), FullBaseSnapshotID: strings.Repeat("b", 64)}
			if tc.explicit {
				input.FullReview = &disabled
			}
			out, err := NewClientWithBaseURL(ConfigStore{}, server.URL).StartWorkspace(context.Background(), "token", input)
			if len(inputs) != tc.calls {
				t.Fatalf("got %d submissions, want %d", len(inputs), tc.calls)
			}
			if tc.calls == 2 {
				if err != nil || out.Early == nil || out.Early.Status != "ready" {
					t.Fatalf("fallback: %+v %v", out, err)
				}
				if inputs[0].FullReview != nil || inputs[1].FullReview == nil || *inputs[1].FullReview || inputs[1].ReviewID != input.ReviewID || inputs[1].FullSnapshotID != input.FullSnapshotID || inputs[1].FullBaseSnapshotID != input.FullBaseSnapshotID {
					t.Fatalf("fallback changed request identity: %+v", inputs)
				}
			} else if err == nil {
				t.Fatal("unrelated failure was swallowed")
			}
		})
	}
}

func TestWorkspaceReplaysOnlyTheOriginalPayloadAfterResponseLoss(t *testing.T) {
	for _, name := range []string{"compatibility rejection on replay", "client timeout", "caller canceled"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var bodies []string
			client := NewClientWithBaseURL(ConfigStore{}, "https://example.test")
			client.HTTP.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				raw, err := io.ReadAll(req.Body)
				if err != nil {
					return nil, err
				}
				bodies = append(bodies, string(raw))
				if len(bodies) == 1 {
					switch name {
					case "caller canceled":
						cancel()
						return nil, io.EOF
					case "client timeout":
						return nil, context.DeadlineExceeded
					default:
						return nil, io.EOF
					}
				}
				if name == "compatibility rejection on replay" {
					return &http.Response{StatusCode: http.StatusBadRequest, Body: io.NopCloser(strings.NewReader(`{"error":"invalid_evidence_context"}`))}, nil
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"id":"accepted-review"}`))}, nil
			})
			got, err := client.StartWorkspace(ctx, "token", WorkspaceSubmission{ReviewID: "same-request", EvidenceContext: true})
			if name == "caller canceled" {
				if err == nil || len(bodies) != 1 {
					t.Fatalf("canceled submission retried: %d calls, %v", len(bodies), err)
				}
				return
			}
			if len(bodies) != 2 || bodies[0] != bodies[1] {
				t.Fatalf("expected one replay with the original payload, got %v", bodies)
			}
			if name == "compatibility rejection on replay" {
				var response *ResponseError
				if !errors.As(err, &response) || response.Code != "invalid_evidence_context" {
					t.Fatalf("expected the replay's rejection, got %v", err)
				}
			} else if err != nil || got.ID != "accepted-review" {
				t.Fatalf("expected existing review after client timeout, got %+v, %v", got, err)
			}
		})
	}
}
