package adversarylabs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/doomerlabs/doomer/pkg/sourcesnapshot"
)

func TestBlobProofProtocolVector(t *testing.T) {
	response, err := blobPossessionResponse(strings.Repeat("ab", 32), "1c49052c919589d56eaff50253fc3f494ff2563245f8d1331336ad676026b3fa", len([]byte("private uncommitted source\n")), []byte("private uncommitted source\n"))
	if err != nil || response != "ed027299867e5e378b384d2c7d7d5c37d2b8ef3ef0c0b4d1e1a774b5983f96ae" {
		t.Fatalf("incompatible proof: %s %v", response, err)
	}
}
func TestWorkspacePossessionProofSkipsOnlyVerifiedUploads(t *testing.T) {
	for _, mode := range []string{"reuse", "rejected", "unavailable", "invalid acknowledgement"} {
		t.Run(mode, func(t *testing.T) {
			data := []byte("private dirty file\n")
			hash := sourcesnapshot.Digest(data)
			uploads, reported := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/test-pack" && r.Header.Get("Authorization") != "Bearer second-owner" {
					t.Error("wrong identity")
				}
				if handleTestPack(t, w, r, func(b []byte) { uploads++; reported = len(b) }) {
					return
				}
				switch r.URL.Path {
				case "/api/v1/repositories":
					json.NewEncoder(w).Encode(map[string]any{"repository_id": "repo", "snapshot_id": "snapshot", "proof_url": "/api/v1/repositories/proofs", "pack_url": "/api/v1/repositories/packs", "missing": []map[string]any{{"sha256": hash, "size": len(data), "upload_url": "/api/v1/repositories/blobs/" + hash}}})
				case "/api/v1/repositories/proofs":
					if mode == "unavailable" {
						w.WriteHeader(503)
						return
					}
					var input struct {
						Action string `json:"action"`
						Proofs []struct {
							ID       string `json:"id"`
							Response string `json:"response"`
						} `json:"proofs"`
					}
					if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
						t.Error(err)
					}
					if input.Action == "challenge" {
						json.NewEncoder(w).Encode(map[string]any{"algorithm": "hmac-sha256-full-file-v1", "challenges": []blobChallenge{{ID: "11111111-1111-4111-8111-111111111111", Nonce: strings.Repeat("ab", 32), SHA: hash, Size: len(data)}}})
					} else {
						expected, _ := blobPossessionResponse(strings.Repeat("ab", 32), hash, len(data), data)
						if len(input.Proofs) != 1 || input.Proofs[0].Response != expected {
							t.Error("client did not prove full content")
						}
						reused := []string{}
						if mode == "reuse" {
							reused = append(reused, hash)
						} else if mode == "invalid acknowledgement" {
							reused = append(reused, strings.Repeat("f", 64))
						}
						json.NewEncoder(w).Encode(map[string]any{"reused": reused})
					}
				case "/api/v1/repositories/blobs/" + hash:
					uploads++
					w.Write([]byte(`{}`))
				case "/api/v1/repositories/repo/snapshots/snapshot/finalize":
					json.NewEncoder(w).Encode(map[string]any{"repository_id": "repo", "snapshot_id": "snapshot", "complete": true})
				default:
					t.Errorf("unexpected route %s", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			client := NewClientWithBaseURL(ConfigStore{}, server.URL+"/api")
			_, err := client.UploadWorkspaceIncremental(context.Background(), "second-owner", "private/repo", []sourcesnapshot.Entry{{Path: "file.txt", Hash: hash, Mode: "100644"}}, map[string][]byte{hash: data}, nil, func(stage string, files, bytes int, _ time.Duration) {
				if stage == "complete" {
					reported = bytes
				}
			})
			if mode == "unavailable" || mode == "invalid acknowledgement" {
				if err == nil || uploads != 0 {
					t.Fatal("protocol error must not fall back to uploads")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			expected := 1
			if mode == "reuse" {
				expected = 0
			}
			if uploads != expected || (expected > 0 && reported <= 0) || (expected == 0 && reported != 0) {
				t.Fatalf("uploads=%d bytes=%d", uploads, reported)
			}
		})
	}
}
