package adversarylabs

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/doomerlabs/doomer/pkg/sourcesnapshot"
)

func TestMultipartTriagePreservesBytesAndRequiresFinalAcknowledgement(t *testing.T) {
	for _, complete := range []bool{true, false} {
		t.Run(map[bool]string{true: "complete", false: "incomplete"}[complete], func(t *testing.T) {
			diff := strings.Repeat("☃\"\\\n", 24000)
			var assembled bytes.Buffer
			parts := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				if len(raw) > 160*1024 {
					t.Error("oversized request")
				}
				if r.Header.Get("Authorization") != "Bearer test" {
					t.Error("missing auth")
				}
				var body struct {
					Action   string       `json:"action"`
					Data     string       `json:"data"`
					SHA256   string       `json:"sha256"`
					DiffHash string       `json:"diff_hash"`
					Parts    []triagePart `json:"parts"`
				}
				if err := json.Unmarshal(raw, &body); err != nil {
					t.Fatal(err)
				}
				if body.Action == "part" {
					data, err := base64.StdEncoding.DecodeString(body.Data)
					if err != nil {
						t.Fatal(err)
					}
					if sourcesnapshot.Digest(data) != body.SHA256 {
						t.Error("bad part hash")
					}
					assembled.Write(data)
					parts++
					_ = json.NewEncoder(w).Encode(triagePart{body.SHA256, len(data)})
					return
				}
				if body.Action != "complete" || parts != len(body.Parts) || assembled.String() != diff {
					t.Error("incomplete assembly")
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"complete": complete, "diff_hash": body.DiffHash, "areas": []any{}})
			}))
			defer server.Close()
			_, err := (Client{BaseURL: server.URL}).TriageWorkspace(t.Context(), "test", "review-id", diff)
			if (err == nil) != complete {
				t.Fatalf("complete=%v error=%v", complete, err)
			}
		})
	}
}
func TestTriagePreservesStructuredFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(413)
		_, _ = w.Write([]byte(`{"error":"triage_diff_too_large"}`))
	}))
	defer server.Close()
	_, err := (Client{BaseURL: server.URL}).TriageWorkspace(t.Context(), "test", "review-id", "diff")
	if err == nil || !strings.Contains(err.Error(), "triage_diff_too_large") {
		t.Fatalf("lost server error: %v", err)
	}
}

func TestTriageUsesSerializedSizeForEscapedSmallDiff(t *testing.T) {
	diff := strings.Repeat("\x00", 40000)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if len(raw) > 160*1024 {
			t.Fatal("escaped body exceeded endpoint limit")
		}
		var body struct {
			Action   string `json:"action"`
			Data     string `json:"data"`
			SHA256   string `json:"sha256"`
			DiffHash string `json:"diff_hash"`
		}
		_ = json.Unmarshal(raw, &body)
		requests++
		if body.Action == "part" {
			data, _ := base64.StdEncoding.DecodeString(body.Data)
			_ = json.NewEncoder(w).Encode(triagePart{body.SHA256, len(data)})
			return
		}
		if body.Action != "complete" {
			t.Fatal("did not select multipart")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"complete": true, "diff_hash": body.DiffHash, "areas": []any{}})
	}))
	defer server.Close()
	if _, err := (Client{BaseURL: server.URL}).TriageWorkspace(t.Context(), "test", "review", diff); err != nil {
		t.Fatal(err)
	}
	if requests != 3 {
		t.Fatalf("requests=%d", requests)
	}
}

func TestLocalTriageBudgetHasStructuredCode(t *testing.T) {
	_, err := (Client{}).TriageWorkspace(t.Context(), "", "review", strings.Repeat("x", maxTriageBytes+1))
	var response *ResponseError
	if !errors.As(err, &response) || response.Code != "triage_diff_too_large" {
		t.Fatalf("missing code: %v", err)
	}
}
