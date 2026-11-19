package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	watching "github.com/doomerlabs/doomer/internal/watch"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
)

func TestMDMManagedProfileRequiresExplicitPreauthorization(t *testing.T) {
	p := mdmProfile{API: "https://doomer.example/api/", Email: " Jane@Example.com ", Key: "doomer_mdm_" + strings.Repeat("x", 43),
		Agents: []string{"codex", "claude", "codex"}, Uploads: true, Commands: true}
	raw, _ := json.Marshal(p)
	got, err := parseMDMProfile(raw)
	if err != nil || got.Email != "jane@example.com" || got.API != "https://doomer.example/api" || !slices.Equal(got.Agents, []string{"claude", "codex"}) {
		t.Fatalf("unexpected profile normalization: %v", err)
	}
	for _, change := range []func(*mdmProfile){
		func(p *mdmProfile) { p.Uploads = false },
		func(p *mdmProfile) { p.Commands = false },
		func(p *mdmProfile) { p.Key = "" },
		func(p *mdmProfile) { p.Email = "invalid" },
		func(p *mdmProfile) { p.Agents = []string{"other"} },
		func(p *mdmProfile) { p.API = "http://doomer.example" },
	} {
		invalid := p
		change(&invalid)
		raw, _ := json.Marshal(invalid)
		if _, err := parseMDMProfile(raw); err == nil {
			t.Fatal("invalid managed profile accepted")
		}
	}
}

func TestMDMEnrollmentReusesAssignmentAndKeepsEnrollmentKeyOutOfState(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	p := mdmProfile{Email: "jane@example.com", Agents: []string{"codex"}, Key: "doomer_mdm_" + strings.Repeat("k", 43)}
	now := time.Now().UTC().Truncate(time.Second)
	token := "adv_cli_" + strings.Repeat("c", 43)
	const clientID = "34d8aa4e-7c49-4dff-ab3d-abe4fbb155f4"
	calls := 0
	var installID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" || r.URL.Path != "/api/v1/auth/mdm/enroll" || r.Header.Get("Authorization") != "Bearer "+p.Key {
			t.Error("unexpected request")
		}
		var body struct {
			Email  string   `json:"email"`
			ID     string   `json:"installation_id"`
			Agents []string `json:"agents"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Email != p.Email || !slices.Equal(body.Agents, p.Agents) || !mdmUUID.MatchString(body.ID) {
			t.Error("incorrect assignment payload")
		}
		if calls == 1 {
			installID = body.ID
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if body.ID != installID {
			t.Error("retry changed the device assignment")
		}
		_ = json.NewEncoder(w).Encode(adversarylabs.Auth{Token: token, ClientID: clientID, ExpiresAt: now.Add(time.Hour).Format(time.RFC3339)})
	}))
	defer server.Close()
	p.API = server.URL + "/api"
	if err := enrollMDM(ctx, p, home, now); !errors.Is(err, errMDMDenied) {
		t.Fatalf("expected generic denial, got %v", err)
	}
	statePath := filepath.Join(home, "state.json")
	if _, err := os.Stat(statePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("denied enrollment wrote consent: %v", err)
	}
	for _, at := range []time.Time{now, now.Add(time.Minute)} {
		if err := enrollMDM(ctx, p, home, at); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 2 {
		t.Fatalf("live credential should avoid another enrollment, got %d calls", calls)
	}
	for _, name := range []string{"state.json", "installation.json", "credentials/config.json"} {
		raw, err := os.ReadFile(filepath.Join(home, filepath.FromSlash(name)))
		if err != nil || bytes.Contains(raw, []byte(p.Key)) {
			t.Fatalf("unexpected data at %s: %v", name, err)
		}
	}
	var state agentHookState
	if err := watching.Read(statePath, &state); err != nil {
		t.Fatal(err)
	}
	if cfg, err := agentHookConsent(state, p.API, "codex", ""); err != nil || !cfg.AllRepositories {
		t.Fatalf("managed approval missing: %v", err)
	}
	if _, err := agentHookConsent(state, p.API, "claude", ""); err == nil {
		t.Fatal("managed approval leaked to another agent")
	}
}

func TestMDMGeneratedHooksUseGoAndKeepClaudeScope(t *testing.T) {
	for _, agent := range []string{"codex", "claude"} {
		var out bytes.Buffer
		if err := writeMDMAgentConfig(&out, agent, ""); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), mdmExecutable+" mdm hook --agent "+agent) || strings.Contains(out.String(), "doomer.js") {
			t.Fatalf("%s hook does not invoke Go directly", agent)
		}
		if agent == "claude" && !strings.Contains(out.String(), "watch results --control *") {
			t.Fatal("missing scoped Claude rules")
		}
	}
}
