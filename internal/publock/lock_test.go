package publock

import (
	"os"
	"testing"
	"time"
)

func TestSameDigestSerializes(t *testing.T) {
	root := t.TempDir()
	first, err := Acquire(root, "sha256:test")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan *Lock, 1)
	go func() { l, _ := Acquire(root, "sha256:test"); done <- l }()
	select {
	case <-done:
		t.Fatal("second publication did not block")
	case <-time.After(50 * time.Millisecond):
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case second := <-done:
		if second == nil {
			t.Fatal("second lock failed")
		}
		_ = second.Close()
	case <-time.After(2 * time.Second):
		t.Fatal("second publication remained blocked")
	}
}

func TestHeldDoesNotCreateState(t *testing.T) {
	root := t.TempDir()
	held, err := Held(root, "watcher")
	if err != nil || held {
		t.Fatalf("missing lock: %t %v", held, err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("probe created state: %v %v", entries, err)
	}
	lock, err := Acquire(root, "watcher")
	if err != nil {
		t.Fatal(err)
	}
	held, err = Held(root, "watcher")
	if err != nil || !held {
		t.Fatalf("active lock: %t %v", held, err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	held, err = Held(root, "watcher")
	if err != nil || held {
		t.Fatalf("released lock: %t %v", held, err)
	}
}
