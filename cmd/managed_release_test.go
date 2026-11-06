package cmd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/doomerlabs/doomer/internal/managed"
	"github.com/doomerlabs/doomer/internal/watch"
)

type managedTransport func(*http.Request) (*http.Response, error)

func (f managedTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPrepareSignedReleaseManualAndOffline(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture executable is a POSIX script")
	}
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	s, err := managedStore()
	if err != nil {
		t.Fatal(err)
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	originalKey := managed.ReleasePublicKey
	managed.ReleasePublicKey = base64.StdEncoding.EncodeToString(public)
	t.Cleanup(func() { managed.ReleasePublicKey = originalKey })
	executable := []byte("#!/bin/sh\nprintf '%s\\n' '{\"protocol\":1,\"managed_protocol\":2}'\n")
	var compressed bytes.Buffer
	gz := gzip.NewWriter(&compressed)
	tw := tar.NewWriter(gz)
	if err = tw.WriteHeader(&tar.Header{Name: "./doomer", Mode: 0755, Size: int64(len(executable)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	tw.Write(executable)
	tw.Close()
	gz.Close()
	archive := compressed.Bytes()
	digest := sha256.Sum256(archive)
	now := time.Now().UTC()
	catalog := managed.Catalog{Schema: managed.Protocol, Generated: now, Expires: now.Add(time.Hour)}
	manifests := map[string][]byte{}
	add := func(version string) {
		raw, _ := json.Marshal(map[string]any{"schemaVersion": 1, "version": version, "artifacts": map[string]string{fmt.Sprintf("doomer_%s_%s_%s.tar.gz", version, runtime.GOOS, runtime.GOARCH): "sha256:" + hex.EncodeToString(digest[:])}})
		sum := sha256.Sum256(raw)
		manifests[version] = raw
		catalog.Releases = append(catalog.Releases, managed.Release{Version: version, Protocol: managed.Protocol, Published: now.Add(-time.Hour), ManifestSHA256: hex.EncodeToString(sum[:])})
	}
	add("2026.11.6.1")
	offline := false
	transport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = transport })
	http.DefaultTransport = managedTransport(func(r *http.Request) (*http.Response, error) {
		if offline {
			return nil, fmt.Errorf("offline")
		}
		var body []byte
		if r.URL.String() == managed.CatalogURL {
			payload, _ := json.Marshal(catalog)
			body, _ = json.Marshal(managed.SignedCatalog{Payload: base64.StdEncoding.EncodeToString(payload), Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(private, payload))})
		} else if strings.HasSuffix(r.URL.Path, "release-manifest.json") {
			for version, data := range manifests {
				if strings.Contains(r.URL.Path, version) {
					body = data
				}
			}
		} else {
			body = archive
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header), Request: r}, nil
	})
	bootstrap := filepath.Join(t.TempDir(), "SKILL.md")
	policy := managed.Policy{Protocol: managed.Protocol, Channel: "manual"}
	text, _ := managed.BootstrapText(policy)
	if err = os.WriteFile(bootstrap, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	prepare := func(allow bool) managed.Task {
		t.Helper()
		handle, _ := managed.NewHandle()
		task := managed.Task{Schema: managed.Protocol, Handle: handle, Bootstrap: managed.Bootstrap{Path: bootstrap, Scope: "project", Policy: policy}}
		state, err := prepareRelease(context.Background(), s, &task, allow)
		if err != nil || state != "ready" {
			t.Fatalf("prepare state=%s err=%v", state, err)
		}
		return task
	}
	first := prepare(true)
	if first.Release != "2026.11.6.1" {
		t.Fatal(first.Release)
	}
	add("2026.11.6.2")
	second := prepare(false)
	if second.Release != first.Release {
		t.Fatal("manual update occurred without approval")
	}
	third := prepare(true)
	if third.Release != "2026.11.6.2" {
		t.Fatal(third.Release)
	}
	offline = true
	fourth := prepare(false)
	if fourth.Release != third.Release {
		t.Fatal("offline selection changed")
	}
	offline = false
	if err := rollbackManaged(s, third); err != nil {
		t.Fatal(err)
	}
	rolledBack := prepare(false)
	if rolledBack.Release != first.Release {
		t.Fatal("rollback selected rejected release", rolledBack.Release)
	}
	var saved struct{ Release managed.Release }
	if err = watch.Read(filepath.Join(s.Root, "selections", watch.Key(bootstrap)+".json"), &saved); err != nil || saved.Release.Version != first.Release {
		t.Fatal(saved, err)
	}
}
