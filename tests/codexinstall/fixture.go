package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"net/http"
	"strings"
	"sync"
)

type fixture struct {
	mu                                              sync.Mutex
	Cancelled, Findings, FailSubmit, PendingReviews bool
	Deep                                            bool
	PortableChoice                                  bool
	DeepPolls                                       int
	Reviews                                         map[string]adversarylabs.WorkspaceSubmission
	Feedback                                        map[string]map[string]any
	Attempts                                        []string
	Request, Challenge                              string
	Approved                                        bool
	Creates, Polls, Uploads                         int
	Events                                          []string
}

func (f *fixture) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Events = append(f.Events, r.Method+" "+r.URL.Path)
	w.Header().Set("Content-Type", "application/json")
	send := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	switch r.URL.Path {
	case "/v1/setup/grants":
		var in struct {
			Request   string `json:"request"`
			Challenge string `json:"codeChallenge"`
			Resumable bool   `json:"resumable"`
		}
		if json.NewDecoder(r.Body).Decode(&in) != nil || in.Request == "" || in.Challenge == "" || !in.Resumable {
			http.Error(w, "invalid setup", 400)
			return
		}
		if f.Request != "" && f.Request != in.Request {
			http.Error(w, "duplicate setup", 409)
			return
		}
		if f.Request == "" {
			f.Creates++
		}
		f.Request = in.Request
		f.Challenge = in.Challenge
		send(map[string]any{"url": "http://" + r.Host + "/cli/setup#request=" + in.Request, "expiresIn": 1800})
	case "/v1/setup/grants/poll":
		var in map[string]string
		_ = json.NewDecoder(r.Body).Decode(&in)
		sum := sha256.Sum256([]byte(in["codeVerifier"]))
		if f.Request == "" || in["request"] != f.Request || base64.RawURLEncoding.EncodeToString(sum[:]) != f.Challenge {
			http.Error(w, "invalid verifier", 403)
			return
		}
		f.Polls++
		if f.Cancelled {
			send(map[string]string{"state": "cancelled"})
			return
		}
		if !f.Approved {
			send(map[string]string{"state": "pending"})
			return
		}
		send(map[string]any{"state": "approved", "token": "fixture-account-token", "account_id": "fixture-account", "portable_state": f.PortableChoice})
	case "/v1/auth/whoami":
		if !f.authorized(r) {
			http.Error(w, "unauthorized", 401)
			return
		}
		send(map[string]string{"id": "fixture-account", "type": "user"})
	case "/v1/repositories":
		if !f.authorized(r) {
			http.Error(w, "unauthorized", 403)
			return
		}
		f.Uploads++
		send(map[string]any{"repository_id": "fixture-repository", "snapshot_id": fmt.Sprintf("snapshot-%d", f.Uploads), "complete": true})
	case "/v1/reviews/workspace":
		if !f.authorized(r) {
			http.Error(w, "unauthorized", 403)
			return
		}
		var input adversarylabs.WorkspaceSubmission
		if json.NewDecoder(r.Body).Decode(&input) != nil || input.ReviewID == "" {
			http.Error(w, "invalid submission", 400)
			return
		}
		f.Attempts = append(f.Attempts, input.ReviewID)
		if f.FailSubmit {
			http.Error(w, `{"error":"fixture temporarily unavailable"}`, 503)
			return
		}
		if f.Reviews == nil {
			f.Reviews = map[string]adversarylabs.WorkspaceSubmission{}
		}
		f.Reviews[input.ReviewID] = input
		send(f.review(input.ReviewID))
	case "/v3/installations/observe":
		send(map[string]bool{"accepted": true})
	default:
		if strings.HasPrefix(r.URL.Path, "/v1/reviews/workspace/") {
			if !f.authorized(r) {
				http.Error(w, "unauthorized", 403)
				return
			}
			rest := strings.TrimPrefix(r.URL.Path, "/v1/reviews/workspace/")
			id := strings.TrimSuffix(rest, "/feedback")
			if _, ok := f.Reviews[id]; !ok {
				http.Error(w, "unknown review", 404)
				return
			}
			if strings.HasSuffix(rest, "/feedback") {
				var response map[string]any
				if json.NewDecoder(r.Body).Decode(&response) != nil || response["lead_id"] != strings.Repeat("a", 64) || response["reason"] == "" || (response["status"] != "fixed" && response["status"] != "dismissed" && response["status"] != "deferred") {
					http.Error(w, "invalid response", 400)
					return
				}
				if f.Feedback == nil {
					f.Feedback = map[string]map[string]any{}
				}
				f.Feedback[id] = response
				send(map[string]bool{"accepted": true})
				return
			}
			if f.Deep {
				f.DeepPolls++
			}
			send(f.review(id))
			return
		}
		if r.Method == "POST" {
			f.Uploads++
		}
		http.Error(w, "unexpected fixture endpoint", 404)
	}
}

func (f *fixture) authorized(r *http.Request) bool {
	return f.Approved && !f.Cancelled && r.Header.Get("Authorization") == "Bearer fixture-account-token"
}
func (f *fixture) review(id string) map[string]any {
	if f.PendingReviews {
		return map[string]any{"id": id, "status": "queued", "anonymous": false, "look_closer_enabled": true, "full_review": map[string]any{"enabled": false, "status": "disabled"}}
	}
	if f.Deep {
		full := map[string]any{"enabled": true, "status": "running"}
		if f.DeepPolls >= 2 {
			full["status"] = "completed"
			full["result"] = map[string]any{"summary": "Fixture deep review verified.", "findings": []any{}}
		}
		return map[string]any{"id": id, "status": "completed", "url": "https://example.invalid/reviews/" + id, "anonymous": false, "look_closer_enabled": true, "full_review": full, "early_assessment": map[string]any{"status": "ready", "stage": "analysis", "leads": []any{}}}
	}
	leads := []map[string]any{}
	if f.Findings {
		leads = append(leads, map[string]any{"id": strings.Repeat("a", 64), "path": "hello.txt", "line": 1, "concern": "Verify the greeting content requested by the user is present.", "check": "Read hello.txt and record whether its content matches the requested greeting."})
	}
	return map[string]any{"id": id, "status": "completed", "url": "https://example.invalid/reviews/" + id, "anonymous": false, "look_closer_enabled": true, "full_review": map[string]any{"enabled": false, "status": "disabled"}, "early_assessment": map[string]any{"status": "ready", "stage": "analysis", "leads": leads}}
}
