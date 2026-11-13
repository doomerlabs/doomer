package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

func readSuite(t *testing.T, path string) suiteSummary {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var s suiteSummary
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSuiteConcurrentMembershipAndIsolation(t *testing.T) {
	names := []string{"first-review", "hook-repair", "repeat-onboarding", "read-only"}
	for _, jobs := range []int{1, 4} {
		t.Run(fmt.Sprint(jobs), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "suite.json")
			var mu sync.Mutex
			active, peak := 0, 0
			paths := map[string]bool{}
			runner := func(ctx context.Context, name, path, log string) suiteCase {
				mu.Lock()
				active++
				if active > peak {
					peak = active
				}
				if paths[path] || paths[log] {
					t.Error("output collision")
				}
				paths[path] = true
				paths[log] = true
				mu.Unlock()
				// Different finish order must not change aggregate membership/order.
				if name == names[0] {
					time.Sleep(40 * time.Millisecond)
				} else {
					time.Sleep(5 * time.Millisecond)
				}
				mu.Lock()
				active--
				mu.Unlock()
				return suiteCase{Scenario: name, Report: path, Log: log, Status: "passed", Passed: true}
			}
			if err := executeSuite(context.Background(), path, .2, 5, "api", jobs, names, runner); err != nil {
				t.Fatal(err)
			}
			s := readSuite(t, path)
			if peak != jobs {
				t.Fatalf("peak %d, want %d", peak, jobs)
			}
			for i, r := range s.Results {
				if r.Scenario != names[i] || !r.Passed {
					t.Fatalf("result %d: %+v", i, r)
				}
			}
		})
	}
}

func TestSuiteBudgetReservationsAndReconciliation(t *testing.T) {
	for _, refund := range []bool{false, true} {
		t.Run(fmt.Sprint(refund), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "suite.json")
			names := []string{"a", "b", "c", "d", "e"}
			var mu sync.Mutex
			active, peak := 0, 0
			runner := func(ctx context.Context, name, path, log string) suiteCase {
				mu.Lock()
				active++
				if active > peak {
					peak = active
				}
				mu.Unlock()
				time.Sleep(5 * time.Millisecond)
				mu.Lock()
				active--
				mu.Unlock()
				r := suiteCase{Scenario: name, Status: "passed", Passed: true}
				if refund {
					charge := .125
					r.Charged = &charge
				}
				return r
			}
			err := executeSuite(context.Background(), path, .25, .5, "api", 4, names, runner)
			s := readSuite(t, path)
			if peak > 2 || *s.Charged > .5 {
				t.Fatalf("oversubscribed: peak %d charged %f", peak, *s.Charged)
			}
			expected := 2
			if refund {
				expected = 3
			}
			if err == nil {
				t.Fatal("expected unstarted work")
			}
			for i, r := range s.Results {
				if (i < expected) != r.Passed {
					t.Fatalf("unexpected result: %+v", s.Results)
				}
			}
		})
	}
}

func TestSuiteCancellationAndFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "suite.json")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	active := 0
	ready := make(chan struct{})
	runner := func(ctx context.Context, name, path, log string) suiteCase {
		mu.Lock()
		active++
		if active == 2 {
			close(ready)
		}
		mu.Unlock()
		<-ready
		cancel()
		<-ctx.Done()
		mu.Lock()
		active--
		mu.Unlock()
		return suiteCase{Scenario: name, Status: "cancelled", Error: ctx.Err().Error()}
	}
	if executeSuite(ctx, path, .2, 5, "api", 2, []string{"a", "b", "c"}, runner) == nil {
		t.Fatal("cancellation passed")
	}
	s := readSuite(t, path)
	if active != 0 || s.Results[2].Status != "unstarted" || s.Results[2].Error != "context canceled" {
		t.Fatalf("workers/results not drained: %+v", s)
	}
	// Assertion failure must not stop unrelated scenarios.
	path = filepath.Join(t.TempDir(), "failure.json")
	runner = func(ctx context.Context, name, path, log string) suiteCase {
		return suiteCase{Scenario: name, Status: "failed", Error: "assertion failed"}
	}
	if executeSuite(context.Background(), path, 0, 0, "subscription", 2, []string{"a", "b", "c"}, runner) == nil {
		t.Fatal("failure passed")
	}
	for _, r := range readSuite(t, path).Results {
		if r.Status != "failed" {
			t.Fatalf("unrun result: %+v", r)
		}
	}
}

func TestSuiteRejectsInvalidInputsAndExistingOutput(t *testing.T) {
	for _, value := range []float64{0, -1, math.NaN(), math.Inf(1)} {
		if executeSuite(context.Background(), filepath.Join(t.TempDir(), "s.json"), .2, value, "api", 1, nil, nil) == nil {
			t.Fatal("accepted budget", value)
		}
	}
	if names, err := selectSuiteScenarios(""); err != nil || !reflect.DeepEqual(names, scenarioNames) {
		t.Fatal("default suite membership differs", names, err)
	}
	if _, err := selectSuiteScenarios("install,install"); err == nil {
		t.Fatal("duplicate accepted")
	}
	if _, err := selectSuiteScenarios("../escape"); err == nil {
		t.Fatal("unknown scenario accepted")
	}
	path := filepath.Join(t.TempDir(), "s.json")
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if executeSuite(context.Background(), path, .2, 1, "api", 1, nil, nil) == nil {
		t.Fatal("overwrote report")
	}
}

func TestSuiteCommandCancellationRetainsReport(t *testing.T) {
	dir := t.TempDir()
	path, log := filepath.Join(dir, "report.json"), filepath.Join(dir, "runner.log")
	ready := filepath.Join(dir, "ready")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan suiteCase, 1)
	script := `trap 'printf '\''{"scenario":"install","auth_mode":"api","passed":false,"error":"cancelled after cleanup","usage":{"budget_charged_usd":0.1}}'\'' > "$1"; exit 1' INT
 touch "$2"
 while :; do sleep 0.02; done`
	go func() {
		done <- runSuiteCommand(ctx, "/bin/sh", []string{"-c", script, "test", path, ready}, "install", path, log, .2, "api")
	}()
	deadline := time.After(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("child did not start")
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	select {
	case r := <-done:
		if r.Passed || r.Status != "cancelled" || r.Charged == nil || *r.Charged != .1 {
			t.Fatalf("lost cancellation report: %+v", r)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("child did not exit gracefully")
	}
}

func TestSuiteCommandBadReportsKeepReservation(t *testing.T) {
	for _, data := range []string{`{}`, `{"scenario":"install","auth_mode":"api","passed":true}`, `{"scenario":"wrong","auth_mode":"api","usage":{"budget_charged_usd":0}}`, `{"scenario":"install","auth_mode":"api","usage":{"budget_charged_usd":-1}}`, `{"scenario":"install","auth_mode":"api","usage":{"budget_charged_usd":1}}`} {
		dir := t.TempDir()
		path := filepath.Join(dir, "report.json")
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		r := runSuiteCommand(context.Background(), "/usr/bin/true", nil, "install", path, filepath.Join(dir, "runner.log"), .2, "api")
		if r.Charged == nil || *r.Charged != .2 {
			t.Fatalf("released unknown spend: %+v", r)
		}
	}
}

func TestLocalBuildCancellationCleansHome(t *testing.T) {
	parent := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := runLocal(ctx, config{Harness: "codex", AuthMode: "api", APIKey: "fixture", CandidateVersion: "2026.11.9.3"}, parent)
	if r.Passed || !r.LocalCleaned || r.Error == "" {
		t.Fatalf("cancelled run did not fail and clean up: %+v", r)
	}
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 0 {
		t.Fatalf("left test home after cancellation: %v %v", entries, err)
	}
}

func TestSuiteReportWriteFailureCancelsWorkers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "suite.json")
	if err := os.Mkdir(path+".tmp", 0700); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	completed := 0
	runner := func(ctx context.Context, name, path, log string) suiteCase {
		<-ctx.Done()
		mu.Lock()
		completed++
		mu.Unlock()
		return suiteCase{Scenario: name, Status: "cancelled", Error: ctx.Err().Error()}
	}
	if err := executeSuite(context.Background(), path, .2, 5, "api", 2, []string{"a", "b", "c"}, runner); err == nil {
		t.Fatal("expected aggregate write error")
	}
	if completed != 2 {
		t.Fatalf("did not drain active workers: %d", completed)
	}
}

func TestWorkerCancellationStopsBeforeAgent(t *testing.T) {
	parent := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := runWorkerContext(ctx, config{Harness: "codex", AuthMode: "api", APIKey: "fixture", WorkRoot: parent})
	if r.Passed || r.Error == "" {
		t.Fatalf("cancelled worker did not fail: %+v", r)
	}
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 0 {
		t.Fatalf("cancelled worker left fixture: %v %v", entries, err)
	}
}
