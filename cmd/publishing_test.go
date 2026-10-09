package cmd

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"github.com/doomerlabs/doomer/pkg/namespacesig"
	"github.com/doomerlabs/doomer/pkg/oci"
	"github.com/doomerlabs/doomer/pkg/repository"
)

func publishCommand(t *testing.T, args ...string) []byte {
	t.Helper()
	root := NewRootCommand()
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		t.Fatalf("%v: %v\n%s", args, err, errOut.String())
	}
	return out.Bytes()
}

func TestPrivatePublishingRoundTrip(t *testing.T) {
	store := isolate(t)
	data := t.TempDir()
	t.Setenv("DOOMER_DATA_DIR", data)
	t.Setenv("DOOMER_REGISTRY_NAMESPACE", "")
	remote := newTestOCIRegistry()
	server := httptest.NewServer(remote)
	defer server.Close()
	host := strings.TrimPrefix(server.URL, "http://")
	t.Setenv("DOOMER_REGISTRY_HOST", host)
	rootPublic, rootPrivate, _ := ed25519.GenerateKey(rand.Reader)
	teamPublic, teamPrivate, _ := ed25519.GenerateKey(rand.Reader)
	bundle := namespacesig.TrustBundle{SpecVersion: 1, MediaType: namespacesig.TrustMediaType, Namespace: "my-team", TeamID: "team-1", KeyID: "team-key", PublicKey: base64.StdEncoding.EncodeToString(teamPublic), IssuedAt: "2026-10-08T12:00:00Z", RootKeyID: "root-key"}
	trustMessage := strings.Join([]string{"adversarylabs-namespace-trust-v1", bundle.Namespace, bundle.TeamID, bundle.KeyID, bundle.PublicKey, bundle.IssuedAt, bundle.RootKeyID}, "\n")
	bundle.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(rootPrivate, []byte(trustMessage)))
	rootKey := namespacesig.Root{KeyID: bundle.RootKeyID, PublicKey: base64.StdEncoding.EncodeToString(rootPublic)}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer work-token" {
			t.Errorf("wrong profile credential on %s", r.URL.Path)
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/auth/registry":
			json.NewEncoder(w).Encode(map[string]any{"token": "registry-token", "expires_in": 300})
		case "/v1/registry/sign":
			t.Error("CLI requested a namespace signature")
			w.WriteHeader(http.StatusNotFound)
		case "/v1/registry/sign/root":
			json.NewEncoder(w).Encode(rootKey)
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	if err := store.SetAuth(adversarylabs.AuthKey(api.URL, "work"), adversarylabs.Auth{Token: "work-token", RegistryNamespace: "my-team", RegistryHost: host}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetAuth(adversarylabs.AuthKey(api.URL, "default"), adversarylabs.Auth{Token: "wrong-token", RegistryNamespace: "wrong-team"}); err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	writeProject(t, project)
	os.WriteFile(filepath.Join(project, "CHECKS.md"), []byte("# Checks\n"), 0644)
	// Preflight validates without committing local artifacts.
	publishCommand(t, "pack", project, "--check", "--format", "json")
	if _, err := os.Stat(filepath.Join(data, "repository-v1", "records")); !os.IsNotExist(err) {
		t.Fatalf("preflight created records: %v", err)
	}
	var packed outputEnvelope[packDTO]
	if err := json.Unmarshal(publishCommand(t, "pack", project, "--format", "json"), &packed); err != nil {
		t.Fatal(err)
	}
	if packed.SchemaVersion != 2 || packed.Data.Name != "local/security-reviewer" || len(packed.Data.Files) == 0 {
		t.Fatalf("invalid package output: %+v", packed)
	}
	var pushed outputEnvelope[pushDTO]
	if err := json.Unmarshal(publishCommand(t, "--profile", "work", "--api-url", api.URL, "push", "local/security-reviewer:1.4.2", "--format", "json"), &pushed); err != nil {
		t.Fatal(err)
	}
	if pushed.Data.CanonicalReference != host+"/my-team/security-reviewer:1.4.2" || pushed.Data.Digest != packed.Data.Digest {
		t.Fatalf("wrong publish result: %+v", pushed)
	}
	if len(remote.referrers[pushed.Data.Digest]) != 0 || remote.manifestCount() != 1 {
		t.Fatal("CLI published hosted attachments instead of only uploading the package")
	}
	// Simulate the server's upload callback publishing docs and a signed bundle.
	// The CLI never requests signing or uploads referrers.
	env := namespacesig.Envelope{SpecVersion: 1, MediaType: namespacesig.SignatureMediaType, Registry: host, Repository: "my-team/security-reviewer", Namespace: bundle.Namespace, TeamID: bundle.TeamID, SubjectDigest: pushed.Data.Digest, KeyID: bundle.KeyID, SignedAt: bundle.IssuedAt}
	msg := strings.Join([]string{"adversarylabs-namespace-signature-v1", env.Registry, env.Repository, env.Namespace, env.TeamID, env.SubjectDigest, env.KeyID, env.SignedAt}, "\n")
	env.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(teamPrivate, []byte(msg)))
	signatureBytes, _ := json.Marshal(env)
	trustBytes, _ := json.Marshal(bundle)
	manifestBytes, err := os.ReadFile(filepath.Join(project, "adversary.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	serverPublisher := oci.NewHTTPRegistry()
	serverPublisher.PlainHTTP = true
	ref, err := oci.ParseReference(pushed.Data.CanonicalReference)
	if err != nil {
		t.Fatal(err)
	}
	for _, attachment := range []struct {
		mediaType, title, kind string
		data                   []byte
	}{
		{oci.AdversaryManifestMediaType, "adversary.yaml", "adversary-manifest", manifestBytes},
		{oci.ReadmeMediaType, "README.md", "adversary-readme", []byte("# Security Reviewer\n")},
		{oci.ChecksMediaType, "CHECKS.md", "adversary-checks", []byte("# Checks\n")},
		{namespacesig.SignatureMediaType, "namespace-signature.json", namespacesig.SignatureArtifactKind, signatureBytes},
		{namespacesig.TrustMediaType, "namespace-trust.json", namespacesig.TrustArtifactKind, trustBytes},
	} {
		if _, _, err := serverPublisher.PushAttachedReferrer(context.Background(), ref, pushed.Data.Digest, attachment.mediaType, attachment.title, attachment.kind, attachment.data); err != nil {
			t.Fatal(err)
		}
	}

	// A separate installation downloads, verifies, and stores the signed content.
	pulledData := t.TempDir()
	t.Setenv("DOOMER_DATA_DIR", pulledData)
	var pulled outputEnvelope[pullDTO]
	if err := json.Unmarshal(publishCommand(t, "--profile", "work", "--api-url", api.URL, "pull", pushed.Data.CanonicalReference, "--format", "json"), &pulled); err != nil {
		t.Fatal(err)
	}
	repo := repository.Repository{Root: filepath.Join(pulledData, "repository-v1"), DefaultRegistry: host}
	if !repo.HasVerifiedNamespaceSignature(pulled.Data.Digest, host, "my-team/security-reviewer") {
		t.Fatal("namespace signature was not verified and cached")
	}
	if _, err := repo.Resolve(host + "/my-team/security-reviewer:1.4.2"); err != nil {
		t.Fatal(err)
	}
	publishCommand(t, "artifacts", "list", "--format", "json")
	publishCommand(t, "artifacts", "inspect", pulled.Data.Digest)
	publishCommand(t, "artifacts", "check")
	// Repeating a pull refreshes references without downloading another record.
	publishCommand(t, "--profile", "work", "--api-url", api.URL, "pull", pushed.Data.CanonicalReference)
	for _, mediaType := range []string{oci.AdversaryManifestMediaType, oci.ReadmeMediaType, oci.ChecksMediaType, namespacesig.SignatureMediaType, namespacesig.TrustMediaType} {
		found := false
		for _, d := range remote.referrers[pushed.Data.Digest] {
			if d.ArtifactType == mediaType {
				found = true
			}
		}
		if !found {
			t.Errorf("missing published referrer %s", mediaType)
		}
	}
}

func TestPublishingCredentialsDoNotCrossProfilesOrServices(t *testing.T) {
	store := isolate(t)
	if err := store.SetAuth(adversarylabs.AuthKey(adversarylabs.DefaultAPIURL, "work"), adversarylabs.Auth{Token: "production-token"}); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []struct{ api, profile string }{{adversarylabs.DefaultAPIURL, "other"}, {"https://other.example/api", "work"}} {
		if _, ok, err := scopedAuth(store, scope.api, scope.profile, adversarylabs.DefaultRegistry); err != nil || ok {
			t.Fatalf("unexpected credential for %s/%s: found=%t err=%v", scope.api, scope.profile, ok, err)
		}
	}
}
