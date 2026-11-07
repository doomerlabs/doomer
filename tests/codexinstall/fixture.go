package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"sync"
)

type fixture struct {
	mu                      sync.Mutex
	Request, Challenge      string
	Approved                bool
	Creates, Polls, Uploads int
	Events                  []string
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
		f.Request = in.Request
		f.Challenge = in.Challenge
		f.Creates++
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
		if !f.Approved {
			send(map[string]string{"state": "pending"})
			return
		}
		send(map[string]any{"state": "approved", "token": "fixture-account-token", "account_id": "fixture-account", "portable_state": false})
	case "/v1/auth/whoami":
		if r.Header.Get("Authorization") != "Bearer fixture-account-token" {
			http.Error(w, "unauthorized", 401)
			return
		}
		send(map[string]string{"id": "fixture-account", "type": "user"})
	case "/v3/installations/observe":
		send(map[string]bool{"accepted": true})
	default:
		if r.Method == "POST" {
			f.Uploads++
		}
		http.Error(w, "unexpected fixture endpoint", 404)
	}
}
