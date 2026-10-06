package snapshot

import (
	"os"
	"strings"
	"testing"
)

func TestSharedV1WireFixture(t *testing.T) {
	data, e := os.ReadFile("../../testdata/source-snapshot-v1.json")
	if e != nil {
		t.Fatal(e)
	}
	expected, e := os.ReadFile("../../testdata/source-snapshot-v1.sha256")
	if e != nil {
		t.Fatal(e)
	}
	if Digest(data) != strings.TrimSpace(string(expected)) {
		t.Fatal("wire digest changed")
	}
	m, e := Decode(data)
	if e != nil {
		t.Fatal(e)
	}
	if string(m.Blobs[m.Target[0].Hash]) != "after\r\n" || m.Target[1].Mode != "100755" {
		t.Fatal("wire source changed")
	}
}
