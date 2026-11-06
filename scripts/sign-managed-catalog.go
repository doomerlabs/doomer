//go:build ignore

// Sign a managed channel only after verifying its release manifest and prior
// catalog. The private key is supplied by the protected release environment.
package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/doomerlabs/doomer/internal/managed"
)

func main() {
	manifestPath := flag.String("manifest", "dist/release-manifest.json", "verified release manifest")
	previousPath := flag.String("previous", "", "previous signed catalog")
	output := flag.String("output", "dist/managed.json", "signed output")
	flag.Parse()
	if err := run(*manifestPath, *previousPath, *output); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(manifestPath, previousPath, output string) error {
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	var manifest struct {
		Version         string `json:"version"`
		SourceDateEpoch string `json:"sourceDateEpoch"`
	}
	if err = json.Unmarshal(raw, &manifest); err != nil {
		return err
	}
	key, err := base64.StdEncoding.DecodeString(os.Getenv("DOOMER_RELEASE_SIGNING_KEY"))
	if err != nil || len(key) != ed25519.PrivateKeySize {
		return fmt.Errorf("release signing key is missing or invalid")
	}
	public, err := base64.StdEncoding.DecodeString(managed.ReleasePublicKey)
	if err != nil || !ed25519.PublicKey(public).Equal(ed25519.PrivateKey(key).Public()) {
		return fmt.Errorf("release signing key does not match the embedded public key")
	}
	now := time.Now().UTC()
	catalog := managed.Catalog{Schema: managed.Protocol}
	if previousPath != "" {
		previous, err := os.ReadFile(previousPath)
		if err != nil {
			return err
		}
		// Verify historical catalog at its issuance time to allow renewal after an
		// expiry. Clients still enforce its actual expiry when consuming updates.
		var envelope managed.SignedCatalog
		var unsigned managed.Catalog
		if json.Unmarshal(previous, &envelope) != nil {
			return fmt.Errorf("invalid previous catalog")
		}
		payload, err := base64.StdEncoding.DecodeString(envelope.Payload)
		if err != nil {
			return err
		}
		if json.Unmarshal(payload, &unsigned) != nil {
			return fmt.Errorf("invalid previous payload")
		}
		catalog, err = managed.VerifyCatalog(previous, managed.ReleasePublicKey, unsigned.Generated)
		if err != nil {
			return err
		}
	}
	sum := sha256.Sum256(raw)
	release := managed.Release{Version: manifest.Version, Published: now, ManifestSHA256: hex.EncodeToString(sum[:]), Protocol: managed.Protocol}
	found := false
	for _, old := range catalog.Releases {
		if old.Version == release.Version {
			if old.ManifestSHA256 != release.ManifestSHA256 {
				return fmt.Errorf("immutable release manifest changed")
			}
			found = true
		}
	}
	if !found {
		catalog.Releases = append(catalog.Releases, release)
	}
	catalog.Generated = now
	catalog.Expires = now.Add(90 * 24 * time.Hour)
	payload, err := json.Marshal(catalog)
	if err != nil {
		return err
	}
	signed := managed.SignedCatalog{Payload: base64.StdEncoding.EncodeToString(payload), Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(key, payload))}
	data, err := json.Marshal(signed)
	if err != nil {
		return err
	}
	return os.WriteFile(output, data, 0600)
}
