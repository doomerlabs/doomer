package e2eimpact

import (
	"bytes"
	"strings"
	"testing"
)

const probeSource = `package command
func newA() any {
 return &Command{Use: "a", RunE: func() error { println("a"); return nil }}
}
func newB() any {
 return &Command{Use: "b", RunE: func() error { println("b"); return nil }}
}
`

func TestProbeFingerprintMutationBoundaries(t *testing.T) {
	b := ProbeHandler{File: "cmd/commands.go", Function: "newB", Field: "RunE"}
	hash := func(source, shared string, omitted *ProbeHandler) string {
		t.Helper()
		key, err := ProbeFingerprint(map[string][]byte{"cmd/commands.go": []byte(source), "cmd/root.go": []byte(shared)}, omitted)
		if err != nil {
			t.Fatal(err)
		}
		return key
	}
	baseline := hash(probeSource, "shared initialization", &b)
	for _, tc := range []struct {
		name, source, shared string
		unchanged            bool
	}{
		{"omitted body", strings.Replace(probeSource, `println("b")`, `panic("changed")`, 1), "shared initialization", true},
		{"selected body", strings.Replace(probeSource, `println("a")`, `panic("changed")`, 1), "shared initialization", false},
		{"omitted metadata", strings.Replace(probeSource, `Use: "b"`, `Use: "renamed"`, 1), "shared initialization", false},
		{"shared startup", probeSource, "changed initialization", false},
		{"registration", probeSource + "\nfunc register() {}\n", "shared initialization", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if equal := hash(tc.source, tc.shared, &b) == baseline; equal != tc.unchanged {
				t.Fatalf("fingerprint unchanged = %v, want %v", equal, tc.unchanged)
			}
		})
	}
	changed := strings.Replace(probeSource, `println("b")`, `panic("changed")`, 1)
	if hash(probeSource, "shared initialization", nil) == hash(changed, "shared initialization", nil) {
		t.Fatal("full source identity ignored a handler mutation")
	}
}

func TestProbeHandlerRejectsAmbiguity(t *testing.T) {
	for _, source := range []string{
		`package p; func newB() {}`,
		`package p; func newB() { _ = C{RunE: handler} }`,
		`package p; func newB() { _ = C{RunE: func(){}}; _ = C{RunE: func(){}} }`,
		`package p; func newB() { _ = func() { _ = C{RunE: func(){}} } }`,
	} {
		if _, err := ReplaceProbeHandler([]byte(source), ProbeHandler{"cmd/b.go", "newB", "RunE"}, "{}"); err == nil {
			t.Fatalf("accepted missing or ambiguous callback: %s", source)
		}
	}
	if _, err := ProbeFingerprint(map[string][]byte{"cmd/b.go": []byte(probeSource)}, &ProbeHandler{"cmd/missing.go", "newB", "RunE"}); err == nil {
		t.Fatal("accepted a missing file")
	}
}

func TestTrapPreservesImportsAndOtherHandlers(t *testing.T) {
	handler := ProbeHandler{"cmd/commands.go", "newB", "RunE"}
	trapped, err := TrapProbeHandler([]byte(probeSource), handler, "reached")
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(probeSource, `{ println("b"); return nil }`, `{ panic("reached"); println("b"); return nil }`, 1)
	if !bytes.Equal(trapped, []byte(want)) {
		t.Fatalf("changed source outside targeted callback:\n%s", trapped)
	}
}
