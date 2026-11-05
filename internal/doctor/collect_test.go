package doctor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAllowlistedStateAndRedaction(t *testing.T) {
	root := t.TempDir()
	c := collector{repo: filepath.Join(root, "repo"), home: root}
	dirty := object{"path": c.repo + "/file", "home": root + "/other", "url": "https://user:password@example.com/review/123?token=secret#key", "token": "adv_cli_secret-value", "owner": []any{object{"session": "session-123"}}}
	data, _ := json.Marshal(c.clean(dirty, 0))
	for _, excluded := range []string{root, "password", "secret-value", "?token", "#key"} {
		if strings.Contains(string(data), excluded) {
			t.Fatalf("leaked %s: %s", excluded, data)
		}
	}
	if !strings.Contains(string(data), "session-123") || !strings.Contains(string(data), "$REPO") {
		t.Fatalf("lost useful diagnostics: %s", data)
	}
	state := diagnostic(object{"At": "now", "Event": "event", "Source": "secret code", "Owner": object{"session": "s", "prompt": "private"}, "Conflict": object{"Reason": "conflict", "patch": "secret"}})
	data, _ = json.Marshal(state)
	if strings.Contains(string(data), "secret") || strings.Contains(string(data), "private") {
		t.Fatal(string(data))
	}
}

func TestReadFileRejectsSymlinkParentsAndOversizedFiles(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "file.json"), []byte(`{"secret":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	if data := readFile(root, "linked/file.json"); data != nil {
		t.Fatal("followed symlink")
	}
	if err := os.WriteFile(filepath.Join(root, "large.json"), make([]byte, MaxBytes+1), 0600); err != nil {
		t.Fatal(err)
	}
	if data := readFile(root, "large.json"); data != nil {
		t.Fatal("read oversized file")
	}
}

func TestOriginIdentity(t *testing.T) {
	for input, want := range map[string]string{"https://user:pass@github.com/org/repo.git?token=x#frag": "github.com/org/repo", "git@github.com:org/repo.git": "github.com/org/repo", "/local/repo": "[local or unrecognized remote]"} {
		if got := originIdentity(input); got != want {
			t.Errorf("%s: %s", input, got)
		}
	}
}

func TestDeepStateIsBounded(t *testing.T) {
	c := collector{repo: "/repo", home: "/home/test"}
	nested := object{"value": "end"}
	for range 30 {
		nested = object{"nested": nested}
	}
	if _, err := json.Marshal(c.clean(nested, 0)); err != nil {
		t.Fatal(err)
	}
}
