package e2eimpact

import (
	"bytes"
	"strings"
	"testing"
)

func TestBoundedHandlerSourceIsIndependentOfRemovedBody(t *testing.T) {
	source := []byte(`package p
import "fmt"
func command() any { return C{RunE: func() error { fmt.Println("old"); return nil }} }
`)
	handler := ProbeHandler{File: "cmd/test.go", Function: "command", Field: "RunE"}
	first, err := BoundedHandlerSource(source, handler)
	if err != nil {
		t.Fatal(err)
	}
	changed := []byte(strings.Replace(string(source), `fmt.Println("old")`, "fmt.Println(\"new\")\n// Extra lines must not move the surviving code.\nfmt.Println(\"more\")", 1))
	second, err := BoundedHandlerSource(changed, handler)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("excluded body changed compiled source:\n%s\n%s", first, second)
	}
	if !strings.Contains(string(first), `_ "fmt"`) || strings.Contains(string(first), `Println`) {
		t.Fatalf("import initialization or body removal incorrect: %s", first)
	}
	metadata := bytes.Replace(source, []byte("return C{"), []byte(`return C{Use: "renamed",`), 1)
	third, err := BoundedHandlerSource(metadata, handler)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first, third) {
		t.Fatal("command metadata was excluded")
	}
}
