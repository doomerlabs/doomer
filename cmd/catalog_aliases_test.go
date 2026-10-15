package cmd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doomerlabs/doomer/internal/application"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
)

func TestPullRejectsPrivateProjectCatalogCollision(t *testing.T) {
	store := isolate(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/auth/whoami" || r.Header.Get("Authorization") != "Bearer token" {
			t.Error("unexpected account request")
		}
		json.NewEncoder(w).Encode(adversarylabs.WhoamiResponse{Teams: []adversarylabs.Team{{Slug: "go"}}})
	}))
	defer server.Close()
	if err := store.SetAuth(adversarylabs.AuthKey(server.URL, "work"), adversarylabs.Auth{Token: "token"}); err != nil {
		t.Fatal(err)
	}
	app := &application.App{Deps: application.Dependencies{Auth: store, RegistryHost: "registry.doomer.ai"}}
	if _, err := resolvePullReference(context.Background(), "go/reviewer", server.URL, "work", app); err == nil || !strings.Contains(err.Error(), "ambiguous reference") {
		t.Fatalf("expected ambiguous namespace: %v", err)
	}
	for _, value := range []string{"library/go/reviewer", "registry.doomer.ai/go/reviewer"} {
		got, err := resolvePullReference(context.Background(), value, server.URL, "work", app)
		if err != nil || got != value {
			t.Fatalf("explicit ref %q = %q, %v", value, got, err)
		}
	}
}

func TestPublishingPreflightDoesNotRecoverTransactions(t *testing.T) {
	isolate(t)
	root := t.TempDir()
	t.Setenv("DOOMER_DATA_DIR", root)
	transactions := filepath.Join(root, "repository-v1", "transactions")
	if err := os.MkdirAll(transactions, 0700); err != nil {
		t.Fatal(err)
	}
	pending := filepath.Join(transactions, "pending.json")
	before := []byte("incomplete transaction")
	if err := os.WriteFile(pending, before, 0600); err != nil {
		t.Fatal(err)
	}
	app := &application.App{Deps: application.Dependencies{RegistryHost: "registry.doomer.ai"}}
	if err := configurePublishing(app, false); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(pending)
	if err != nil || string(after) != string(before) {
		t.Fatalf("preflight changed pending transaction: %v", err)
	}
}
