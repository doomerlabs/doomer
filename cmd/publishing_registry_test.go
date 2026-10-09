package cmd

import (
	"encoding/json"
	"fmt"
	"github.com/doomerlabs/doomer/pkg/oci"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type testOCIRegistry struct {
	mu                   sync.Mutex
	blobs                map[string][]byte
	blobContentTypes     map[string]string
	manifests            map[string][]byte
	manifestDigests      map[string][]byte
	manifestContentTypes map[string]string
	referrers            map[string][]oci.Descriptor
}

func newTestOCIRegistry() *testOCIRegistry {
	return &testOCIRegistry{
		blobs:                map[string][]byte{},
		blobContentTypes:     map[string]string{},
		manifests:            map[string][]byte{},
		manifestDigests:      map[string][]byte{},
		manifestContentTypes: map[string]string{},
		referrers:            map[string][]oci.Descriptor{},
	}
}

func (r *testOCIRegistry) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.URL.Path == "/v2/" {
		w.WriteHeader(http.StatusOK)
		return
	}
	if !strings.HasPrefix(req.URL.Path, "/v2/") {
		http.NotFound(w, req)
		return
	}
	path := strings.TrimPrefix(req.URL.Path, "/v2/")
	switch {
	case strings.Contains(path, "/blobs/uploads/") && req.Method == http.MethodPost:
		w.Header().Set("Location", "/v2/"+path+"test-upload")
		w.WriteHeader(http.StatusAccepted)
	case strings.Contains(path, "/blobs/uploads/") && req.Method == http.MethodPut:
		digest := req.URL.Query().Get("digest")
		data, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.blobs[digest] = data
		r.blobContentTypes[digest] = req.Header.Get("Content-Type")
		r.mu.Unlock()
		w.Header().Set("Docker-Content-Digest", digest)
		w.WriteHeader(http.StatusCreated)
	case strings.Contains(path, "/blobs/"):
		_, digest, _ := strings.Cut(path, "/blobs/")
		r.mu.Lock()
		data, ok := r.blobs[digest]
		r.mu.Unlock()
		if !ok {
			http.NotFound(w, req)
			return
		}
		w.Header().Set("Docker-Content-Digest", digest)
		if req.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		_, _ = w.Write(data)
	case strings.Contains(path, "/manifests/") && req.Method == http.MethodPut:
		data, _ := io.ReadAll(req.Body)
		key := manifestKey(path)
		digest := oci.Digest(data)
		r.mu.Lock()
		r.manifests[key] = data
		r.manifestDigests[digest] = data
		r.manifestContentTypes[key] = req.Header.Get("Content-Type")
		if req.Header.Get("Content-Type") == oci.OCIArtifactManifestMediaType {
			var artifact oci.ArtifactManifest
			if err := json.Unmarshal(data, &artifact); err == nil {
				r.referrers[artifact.Subject.Digest] = append(r.referrers[artifact.Subject.Digest], oci.Descriptor{
					MediaType:    oci.OCIArtifactManifestMediaType,
					Digest:       digest,
					Size:         int64(len(data)),
					ArtifactType: artifact.ArtifactType,
				})
			}
		}
		r.mu.Unlock()
		w.Header().Set("Docker-Content-Digest", digest)
		w.WriteHeader(http.StatusCreated)
	case strings.Contains(path, "/manifests/") && req.Method == http.MethodGet:
		key := manifestKey(path)
		_, ref, _ := strings.Cut(path, "/manifests/")
		r.mu.Lock()
		data, ok := r.manifests[key]
		if !ok && strings.HasPrefix(ref, "sha256:") {
			data, ok = r.manifestDigests[ref]
		}
		r.mu.Unlock()
		if !ok {
			http.NotFound(w, req)
			return
		}
		if strings.Contains(req.Header.Get("Accept"), oci.OCIArtifactManifestMediaType) {
			w.Header().Set("Content-Type", oci.OCIArtifactManifestMediaType)
		} else {
			w.Header().Set("Content-Type", oci.ImageManifestMediaType)
		}
		w.Header().Set("Docker-Content-Digest", oci.Digest(data))
		_, _ = w.Write(data)
	case strings.Contains(path, "/referrers/") && req.Method == http.MethodGet:
		_, digest, _ := strings.Cut(path, "/referrers/")
		r.mu.Lock()
		descriptors := append([]oci.Descriptor(nil), r.referrers[digest]...)
		r.mu.Unlock()
		if artifactType := req.URL.Query().Get("artifactType"); artifactType != "" {
			filtered := descriptors[:0]
			for _, descriptor := range descriptors {
				if descriptor.ArtifactType == artifactType {
					filtered = append(filtered, descriptor)
				}
			}
			descriptors = filtered
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(oci.ReferrersResponse{Manifests: descriptors})
	default:
		http.NotFound(w, req)
	}
}

func (r *testOCIRegistry) manifestCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.manifests)
}

func (r *testOCIRegistry) manifest(t *testing.T, key string) []byte {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	data, ok := r.manifests[key]
	if !ok {
		t.Fatalf("manifest %q not found", key)
	}
	return append([]byte(nil), data...)
}

func (r *testOCIRegistry) manifestContentType(key string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.manifestContentTypes[key]
}

func (r *testOCIRegistry) blob(t *testing.T, digest string) []byte {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	data, ok := r.blobs[digest]
	if !ok {
		t.Fatalf("blob %q not found", digest)
	}
	return append([]byte(nil), data...)
}

func (r *testOCIRegistry) blobContentType(digest string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.blobContentTypes[digest]
}

func manifestKey(path string) string {
	repo, ref, _ := strings.Cut(path, "/manifests/")
	return fmt.Sprintf("%s/%s", repo, ref)
}

func writeProject(t *testing.T, dir string) {
	t.Helper()
	files := map[string]string{
		"adversary.yaml": `name: local/security-reviewer
version: 1.4.2
runtime:
  name: node
  version: "22"
  command:
    - dist/index.js
`,
		"README.md":    "# Security Reviewer\n",
		"LICENSE":      "MIT\n",
		"package.json": `{"type":"module"}`,
		"dist/index.js": `import { writeFileSync } from "node:fs";
writeFileSync(process.env.ADVERSARY_OUTPUT, JSON.stringify({protocolVersion:1,result:{adversary:{name:"local/security-reviewer"},target:{},positives:[],observations:[],findings:[],suppressed:{observations:0,findings:0}}}));
`,
	}
	for rel, content := range files {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
}
