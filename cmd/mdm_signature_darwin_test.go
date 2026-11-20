package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMDMRealPackageSignature(t *testing.T) {
	pkg := os.Getenv("DOOMER_TEST_MDM_RUNTIME")
	if pkg == "" {
		t.Skip("set DOOMER_TEST_MDM_RUNTIME to a signed managed package")
	}
	identity := os.Getenv("DOOMER_TEST_MDM_SIGNER")
	if !mdmInstallerIdentity.MatchString(identity) {
		t.Fatal("DOOMER_TEST_MDM_SIGNER must be the expected Developer ID Installer identity")
	}
	packages := []string{pkg}
	if bootstrap := os.Getenv("DOOMER_TEST_MDM_BOOTSTRAP"); bootstrap != "" {
		packages = append(packages, bootstrap)
	}
	for _, path := range packages {
		t.Run(filepath.Base(path), func(t *testing.T) {
			signature, err := runMDMSystemCommand(t.Context(), mdmPkgutil, "--check-signature", path)
			t.Logf("pkgutil output:\n%s", signature)
			if err != nil || !mdmTrustedSigner(signature, identity) {
				t.Fatalf("the macOS updater rejected the signed package: %v", err)
			}
		})
	}
}
