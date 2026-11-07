package managed

import (
	"testing"
	"time"
)

func TestBootstrapDeferralScopeVersionAndExpiry(t *testing.T) {
	s := Store{Root: t.TempDir()}
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	if s.BootstrapDeferred("repo-a", BootstrapVersion, now) {
		t.Fatal("missing deferral suppressed an offer")
	}
	if err := s.DeferBootstrap("repo-a", BootstrapVersion, now); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		identity, version string
		when              time.Time
		want              bool
	}{
		{"repo-a", BootstrapVersion, now.Add(time.Hour), true},
		{"repo-b", BootstrapVersion, now.Add(time.Hour), false},
		{"repo-a", "2026.11.7.1", now.Add(time.Hour), false},
		{"repo-a", BootstrapVersion, now.Add(24 * time.Hour), false},
	} {
		if got := s.BootstrapDeferred(tc.identity, tc.version, tc.when); got != tc.want {
			t.Fatalf("%+v: got %t", tc, got)
		}
	}
}
