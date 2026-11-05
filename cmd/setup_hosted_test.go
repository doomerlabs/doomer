package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/doomerlabs/doomer/internal/application"
	watching "github.com/doomerlabs/doomer/internal/watch"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
)

func TestHostedSetupUsesWebsiteAndExchangesBoundCode(t *testing.T) {
	for _, decision := range []string{"allow", "cancel", "anonymous"} {
		t.Run(decision, func(t *testing.T) {
			var grant setupGrantRequest
			exchanged := false
			var service *httptest.Server
			service = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/setup/grants":
					if r.Header.Get("Authorization") != "Bearer anonymous-credential" {
						t.Error("missing installation credential")
					}
					if err := json.NewDecoder(r.Body).Decode(&grant); err != nil {
						t.Error(err)
					}
					if grant.Profile != "default" || grant.Repositories[0].Checkout != "/repo" {
						t.Error("scope lost")
					}
					_ = json.NewEncoder(w).Encode(map[string]string{"url": service.URL + "/cli/setup#request=opaque"})
				case "/api/v1/auth/cli/exchange":
					var input map[string]string
					_ = json.NewDecoder(r.Body).Decode(&input)
					challenge := sha256.Sum256([]byte(input["code_verifier"]))
					if input["code"] != "one-time-code" || input["redirect_uri"] != grant.Callback || base64.RawURLEncoding.EncodeToString(challenge[:]) != grant.CodeChallenge {
						t.Error("exchange not bound to grant")
					}
					exchanged = true
					_ = json.NewEncoder(w).Encode(adversarylabs.TokenResponse{Token: "cli-token", ClientID: "client-id"})
				default:
					t.Error("unexpected request", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer service.Close()
			scope := setupConsent{Profile: "default", API: service.URL + "/api", Repositories: []setupRepository{{Checkout: "/repo"}}, Agents: []string{"codex"}}
			approved, token, err := hostedSetupConsent(context.Background(), scope, "anonymous-credential", adversarylabs.Client{BaseURL: scope.API}, io.Discard, func(ctx context.Context, link string) error {
				if link != service.URL+"/cli/setup#request=opaque" {
					t.Error("opened wrong page", link)
				}
				callback, _ := url.Parse(grant.Callback)
				q := callback.Query()
				q.Set("decision", "allow")
				if decision == "cancel" {
					q.Set("decision", "cancel")
				}
				if decision == "allow" {
					q.Set("code", "one-time-code")
				}
				callback.RawQuery = q.Encode()
				req, _ := http.NewRequestWithContext(ctx, "GET", callback.String(), nil)
				response, err := http.DefaultClient.Do(req)
				if err != nil {
					return err
				}
				defer response.Body.Close()
				if response.StatusCode != 200 {
					return fmt.Errorf("callback status %d", response.StatusCode)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if approved != (decision != "cancel") {
				t.Fatal("incorrect decision")
			}
			if decision == "allow" {
				if token == nil || token.Token != "cli-token" || !exchanged {
					t.Fatal("CLI not authenticated")
				}
			} else if token != nil || exchanged {
				t.Fatal("unexpected authentication")
			}
		})
	}
}

func TestHostedCallbackRejectsForgedAndRepeatedDecisions(t *testing.T) {
	result := make(chan setupGrantResult, 1)
	scope := setupConsent{Nonce: strings.Repeat("a", 64)}
	handler := hostedApprovalHandler(scope, "127.0.0.1:1234", result)
	for _, path := range []string{"/complete?nonce=bad&decision=allow&code=bad", "/consent?nonce=" + scope.Nonce + "&decision=allow", "/complete?nonce=" + scope.Nonce + "&decision=bad"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("GET", "http://127.0.0.1:1234"+path, nil))
		if response.Code == 200 || len(result) > 0 {
			t.Fatal("forged callback accepted")
		}
	}
	request := "http://127.0.0.1:1234/complete?nonce=" + scope.Nonce + "&decision=allow&code=good"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", request, nil))
	if response.Code != 200 || (<-result).code != "good" {
		t.Fatal("valid callback failed")
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", request, nil))
	if response.Code != 410 || len(result) > 0 {
		t.Fatal("replay accepted")
	}
}

func TestHostedSetupSavesLoginAndConsentInSelectedProfile(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(fmt.Sprintf("expired=%v", expired), func(t *testing.T) {
			store := isolate(t)
			t.Setenv("HOME", t.TempDir())
			t.Setenv("CODEX_HOME", t.TempDir())
			repo := newWatchTestRepo(t)
			var grant setupGrantRequest
			var service *httptest.Server
			service = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/setup/grants") {
					_ = json.NewDecoder(r.Body).Decode(&grant)
					_ = json.NewEncoder(w).Encode(map[string]string{"url": service.URL + "/cli/setup#request=opaque"})
				} else if strings.HasSuffix(r.URL.Path, "/auth/cli/exchange") {
					_ = json.NewEncoder(w).Encode(adversarylabs.TokenResponse{Token: "adv_cli_test", ClientID: "client", Namespace: "alice"})
				} else {
					http.NotFound(w, r)
				}
			}))
			defer service.Close()
			api, profile := service.URL+"/api", "test-profile"
			app := &application.App{Deps: application.Dependencies{Auth: store}}
			if expired {
				if err := store.SetAuth(adversarylabs.AuthKey(api, profile), adversarylabs.Auth{Token: "old-expired", ExpiresAt: "2000-01-01T00:00:00Z"}); err != nil {
					t.Fatal(err)
				}
			}
			other := adversarylabs.Auth{Token: "other-login"}
			if err := store.SetAuth(adversarylabs.AuthKey(api, "other"), other); err != nil {
				t.Fatal(err)
			}
			previous := openSetupBrowser
			defer func() { openSetupBrowser = previous }()
			openSetupBrowser = func(ctx context.Context, _ string) error {
				callback, _ := url.Parse(grant.Callback)
				q := callback.Query()
				q.Set("decision", "allow")
				q.Set("code", "code")
				callback.RawQuery = q.Encode()
				req, _ := http.NewRequestWithContext(ctx, "GET", callback.String(), nil)
				response, err := http.DefaultClient.Do(req)
				if err == nil {
					response.Body.Close()
				}
				return err
			}
			command := newSetupCommand(app, &api, &profile)
			command.SetContext(context.Background())
			command.SetOut(io.Discard)
			_ = command.Flags().Set("browser", "true")
			if err := command.RunE(command, []string{repo}); err != nil {
				t.Fatal(err)
			}
			auth, ok, err := store.ExactAuthE(adversarylabs.AuthKey(api, profile))
			if err != nil || !ok || auth.Token != "adv_cli_test" || auth.Namespace != "alice" {
				t.Fatalf("login not saved: %+v %v", auth, err)
			}
			_, file, _ := watchPaths(api, profile)
			var cfg watching.Config
			if err := watching.Read(file, &cfg); err != nil {
				t.Fatal(err)
			}
			if !watching.ConsentValid(cfg, api, profile, auth.Token) {
				t.Fatal("consent bound to old anonymous credential")
			}
			retained, _, _ := store.ExactAuthE(adversarylabs.AuthKey(api, "other"))
			if retained != other {
				t.Fatal("unrelated profile changed")
			}
		})
	}
}
