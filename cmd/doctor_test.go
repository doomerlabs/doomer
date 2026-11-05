package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func doctorRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", dir).CombinedOutput(); err != nil {
		t.Fatalf("%s: %v", out, err)
	}
	return dir
}
func TestDoctorLocalReportSurvivesBrokenSettings(t *testing.T) {
	store := isolate(t)
	if err := os.WriteFile(filepath.Join(filepath.Dir(store.Path), "settings.yaml"), []byte("[broken:"), 0600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "report.json")
	out, err := execute(t, "", "--api-url", "invalid", "doctor", "--repo", doctorRepo(t), "--no-upload", "--output", output, "--tag", "case=123")
	if err != nil {
		t.Fatalf("%s: %v", out, err)
	}
	info, err := os.Stat(output)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("permissions: %v %v", info, err)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var report map[string]any
	if json.Unmarshal(data, &report) != nil || report["tags"].(map[string]any)["case"] != "123" {
		t.Fatal(string(data))
	}
	if _, err = execute(t, "", "doctor", "--repo", doctorRepo(t), "--no-upload", "--output", output); err == nil {
		t.Fatal("overwrote existing file")
	}
}
func TestDoctorUploadsAndRetainsLocalReportOnFailure(t *testing.T) {
	isolate(t)
	repo := doctorRepo(t)
	var received bool
	succeed := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = true
		if r.URL.Path != "/v1/doctor/reports" || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer doomer_anon_") {
			t.Errorf("unexpected upload: %s", r.URL.Path)
		}
		var report map[string]any
		if err := json.NewDecoder(r.Body).Decode(&report); err != nil || report["schema"] != float64(1) {
			t.Errorf("report: %v", err)
		}
		if succeed {
			_, _ = w.Write([]byte(`{"id":"support-report-123"}`))
			return
		}
		http.Error(w, `{"error":"test failure"}`, http.StatusBadRequest)
	}))
	defer server.Close()
	output := filepath.Join(t.TempDir(), "report.json")
	out, err := execute(t, "", "--api-url", server.URL, "doctor", "--repo", repo, "--output", output)
	if err == nil || !received || !strings.Contains(out, "Report saved") {
		t.Fatalf("%s: %v (received %v)", out, err, received)
	}
	if _, err = os.Stat(output); err != nil {
		t.Fatal(err)
	}
	succeed = true
	out, err = execute(t, "", "--api-url", server.URL, "doctor", "--repo", repo, "--output", filepath.Join(t.TempDir(), "success.json"))
	if err != nil || !strings.Contains(out, "Uploaded private support report support-report-123") {
		t.Fatalf("%s: %v", out, err)
	}
}

func TestDoctorBypassesMissingCheckoutRuntime(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	original := launcherExecutable
	launcherExecutable = func() (string, error) { return filepath.Join(home, ".local/bin/doomer"), nil }
	t.Cleanup(func() { launcherExecutable = original })
	repo := doctorRepo(t)
	putPin(t, repo, "2026.11.3.7")
	var out bytes.Buffer
	handled, err := DispatchCLI(context.Background(), []string{"doctor", "--repo", repo, "--no-upload"}, nil, &out, &out)
	if handled || err != nil {
		t.Fatalf("doctor dispatched to broken runtime: %v %v", handled, err)
	}
}
