package managed

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"time"

	"github.com/doomerlabs/doomer/internal/runtimeversion"
)

const CatalogURL = "https://downloads.doomer.ai/cli/managed.json"

// ReleasePublicKey is the Ed25519 verification key distributed in the launcher.
// A release publisher holds the corresponding private key outside the repository.
//
//go:embed release-key.txt
var ReleasePublicKey string

type Release struct {
	Version        string    `json:"version"`
	Published      time.Time `json:"published"`
	ManifestSHA256 string    `json:"manifest_sha256"`
	Protocol       int       `json:"protocol"`
	Withdrawn      bool      `json:"withdrawn,omitempty"`
}
type Catalog struct {
	Schema    int       `json:"schema"`
	Generated time.Time `json:"generated"`
	Expires   time.Time `json:"expires"`
	Releases  []Release `json:"releases"`
}
type SignedCatalog struct {
	Payload   string `json:"payload"`
	Signature string `json:"signature"`
}

func VerifyCatalog(raw []byte, publicKey string, now time.Time) (Catalog, error) {
	var out Catalog
	key, err := base64.StdEncoding.DecodeString(publicKey)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return out, fmt.Errorf("release verification key is unavailable")
	}
	var envelope SignedCatalog
	if json.Unmarshal(raw, &envelope) != nil {
		return out, fmt.Errorf("invalid signed release catalog")
	}
	payload, err := base64.StdEncoding.DecodeString(envelope.Payload)
	if err != nil {
		return out, fmt.Errorf("invalid catalog payload")
	}
	signature, err := base64.StdEncoding.DecodeString(envelope.Signature)
	if err != nil || !ed25519.Verify(key, payload, signature) {
		return out, fmt.Errorf("release catalog signature mismatch")
	}
	if json.Unmarshal(payload, &out) != nil || out.Schema != Protocol || out.Generated.After(now.Add(5*time.Minute)) || !out.Expires.After(now) {
		return out, fmt.Errorf("release catalog is invalid or expired")
	}
	seen := map[string]bool{}
	for _, r := range out.Releases {
		hash, err := hex.DecodeString(r.ManifestSHA256)
		if !runtimeversion.Release.MatchString(r.Version) || err != nil || len(hash) != sha256.Size || seen[r.Version] || r.Published.IsZero() {
			return Catalog{}, fmt.Errorf("invalid release catalog entry")
		}
		seen[r.Version] = true
	}
	return out, nil
}
func FetchCatalog(ctx context.Context, client *http.Client) (Catalog, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, CatalogURL, nil)
	if err != nil {
		return Catalog{}, err
	}
	req.Header.Set("Cache-Control", "no-cache")
	response, err := client.Do(req)
	if err != nil {
		return Catalog{}, NetworkError("release_check", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		err := fmt.Errorf("release service returned HTTP %d", response.StatusCode)
		if response.StatusCode == http.StatusForbidden {
			host, _ := url.Parse(CatalogURL)
			return Catalog{}, &ConnectionError{Host: host.Hostname(), err: err}
		}
		return Catalog{}, err
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil {
		return Catalog{}, NetworkError("release_check", err)
	}
	if len(data) > 1<<20 {
		return Catalog{}, fmt.Errorf("release catalog exceeds size limit")
	}
	return VerifyCatalog(data, ReleasePublicKey, time.Now())
}
func SelectRelease(c Catalog, p Policy, now time.Time) (Release, error) {
	var eligible []Release
	for _, r := range c.Releases {
		if r.Protocol == Protocol && !r.Withdrawn && p.Eligible(r, now) {
			eligible = append(eligible, r)
		}
	}
	if len(eligible) == 0 {
		return Release{}, fmt.Errorf("no compatible release satisfies the upgrade policy")
	}
	sort.Slice(eligible, func(i, j int) bool { return VersionLess(eligible[j].Version, eligible[i].Version) })
	return eligible[0], nil
}
func VersionLess(a, b string) bool {
	var ay, am, ad, an, by, bm, bd, bn int
	fmt.Sscanf(a, "%d.%d.%d.%d", &ay, &am, &ad, &an)
	fmt.Sscanf(b, "%d.%d.%d.%d", &by, &bm, &bd, &bn)
	aa, bb := []int{ay, am, ad, an}, []int{by, bm, bd, bn}
	for i := range aa {
		if aa[i] != bb[i] {
			return aa[i] < bb[i]
		}
	}
	return false
}
