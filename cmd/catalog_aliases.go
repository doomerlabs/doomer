package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/doomerlabs/doomer/internal/application"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
)

var officialCatalogDomains = map[string]struct{}{
	"cloud": {}, "ci": {}, "container": {}, "deps": {}, "factory": {},
	"go": {}, "infra": {}, "lang": {}, "meta": {}, "review": {},
	"security": {}, "web": {},
}

// canonicalCatalogReference keeps historical catalog spellings working at the
// CLI boundary. Catalog IDs are domain/name; depot/ci was published in early
// examples before the canonical ci/depot ID was established.
func canonicalCatalogReference(value string) string {
	value = strings.TrimSpace(value)
	const legacy = "depot/ci"
	const canonical = "ci/depot"
	if value == legacy {
		value = canonical
	}
	if strings.HasPrefix(value, legacy) && len(value) > len(legacy) {
		switch value[len(legacy)] {
		case ':', '@':
			value = canonical + value[len(legacy):]
		}
	}
	name := value
	if before, _, ok := strings.Cut(name, "@"); ok {
		name = before
	}
	if colon := strings.LastIndex(name, ":"); colon > strings.LastIndex(name, "/") {
		name = name[:colon]
	}
	parts := strings.Split(name, "/")
	if len(parts) == 2 {
		if _, official := officialCatalogDomains[strings.ToLower(parts[0])]; official {
			return "library/" + value
		}
	}
	return value
}

// Catalog shorthand must not hide a private project with the same namespace.
// Explicit registry and library references never need this account lookup.
func resolvePullReference(ctx context.Context, value, apiURL, profile string, app *application.App) (string, error) {
	value = strings.TrimSpace(value)
	canonical := canonicalCatalogReference(value)
	if canonical == value {
		return canonical, nil
	}
	namespace, _, _ := strings.Cut(value, "/")
	deps := app.Dependencies()
	ambiguous := func() (string, error) {
		return "", fmt.Errorf("ambiguous reference %q: %s is also a private project namespace; use %s for the catalog or %s/%s for the private project", value, namespace, canonical, deps.RegistryHost, value)
	}
	if strings.EqualFold(deps.RegistryNS, namespace) {
		return ambiguous()
	}
	if deps.Auth == nil {
		return canonical, nil
	}
	auth, ok, err := scopedAuth(deps.Auth, apiURL, profile, deps.RegistryHost)
	if err != nil {
		return "", err
	}
	if !ok || strings.TrimSpace(auth.Token) == "" {
		return canonical, nil
	}
	if strings.EqualFold(auth.RegistryNamespace, namespace) {
		return ambiguous()
	}
	client := adversarylabs.NewClientWithBaseURL(adversarylabs.ConfigStore{}, apiURL)
	account, err := client.Whoami(ctx, auth.Token)
	if err != nil {
		return "", fmt.Errorf("check private namespace for %q: %w; use an explicit library/ or registry reference", value, err)
	}
	if strings.EqualFold(account.Team.Slug, namespace) {
		return ambiguous()
	}
	for _, team := range account.Teams {
		if strings.EqualFold(team.Slug, namespace) {
			return ambiguous()
		}
	}
	return canonical, nil
}
