package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/doomerlabs/doomer/internal/application"
	"github.com/doomerlabs/doomer/internal/modelreview"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"github.com/doomerlabs/doomer/pkg/repository"
)

type runLifecycleAPI struct {
	adversarylabs.Client
	calls chan adversarylabs.RunUsageReport
}

func (c *runLifecycleAPI) RecordUsage(ctx context.Context, _, _, _ string, report adversarylabs.RunUsageReport) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.calls <- report
	return nil
}

func TestRunLifecycle(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "cancel"}[canceled], func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			deps := lifecycleTestApp(t, repository.Repository{Root: t.TempDir()}, &stdout, &stderr).Dependencies()
			store := deps.Auth.(processAuthStore).ConfigStore
			const apiURL = "https://api.example.test"
			if err := store.SetAuth(adversarylabs.AuthKey(apiURL, "work"), adversarylabs.Auth{Token: "test-token"}); err != nil {
				t.Fatal(err)
			}
			api := &runLifecycleAPI{calls: make(chan adversarylabs.RunUsageReport, 100)}
			deps.API = pullMetricAPIFactory{identity: store.Path, client: api}
			app, err := application.New(deps)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(modelreview.WithUsageCollector(t.Context()))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, `{"output":[{"content":[{"type":"output_text","text":"{}"}]}],"usage":{"input_tokens":123,"output_tokens":45}}`)
			}))
			defer server.Close()
			provider := &modelreview.OpenAIProvider{ModelID: "gpt-5.6-luna", BaseURL: server.URL, Client: server.Client()}
			if _, err := provider.Review(ctx, modelreview.Request{Schema: json.RawMessage(`{}`)}); err != nil {
				t.Fatal(err)
			}
			defer cancel()
			finish := beginRunUsageEvery(ctx, app, apiURL, "work", adversarylabs.RunUsageReport{Adversaries: []string{"./private"}}, 5*time.Millisecond)
			start := <-api.calls
			if start.Action != "start" || len(start.TraceID) != 32 || start.Adversaries[0] != "local" {
				t.Fatalf("start: %+v", start)
			}
			select {
			case heartbeat := <-api.calls:
				if heartbeat.Action != "heartbeat" || heartbeat.TraceID != start.TraceID {
					t.Fatalf("heartbeat: %+v", heartbeat)
				}
			case <-time.After(time.Second):
				t.Fatal("no heartbeat")
			}
			if canceled {
				cancel()
			}
			finish(adversarylabs.RunUsageReport{Outcome: "completed"})
			finish(adversarylabs.RunUsageReport{Outcome: "failed"}) // duplicate cleanup cannot send a second finish
			waitCtx, waitCancel := context.WithTimeout(context.Background(), time.Second)
			defer waitCancel()
			app.WaitBackground(waitCtx)
			var final adversarylabs.RunUsageReport
			for len(api.calls) > 0 {
				final = <-api.calls
			}
			want := "completed"
			if canceled {
				want = "canceled"
			}
			if len(final.ModelUsage) != 1 || *final.ModelUsage[0].InputTokens != 123 || *final.ModelUsage[0].OutputTokens != 45 {
				t.Fatalf("model usage: %+v", final.ModelUsage)
			}
			if start.ModelUsage != nil {
				t.Fatal("start must not claim zero usage")
			}
			if final.Action != "finish" || final.Outcome != want || final.TraceID != start.TraceID || len(final.Spans) == 0 {
				t.Fatalf("finish: %+v", final)
			}
			time.Sleep(15 * time.Millisecond)
			if len(api.calls) != 0 {
				t.Fatal("heartbeat continued after finish")
			}
		})
	}
}

func TestRunUsageFinalizesAfterPostReviewModelCalls(t *testing.T) {
	var stdout, stderr bytes.Buffer
	deps := lifecycleTestApp(t, repository.Repository{Root: t.TempDir()}, &stdout, &stderr).Dependencies()
	store := deps.Auth.(processAuthStore).ConfigStore
	const apiURL = "https://api.example.test"
	if err := store.SetAuth(adversarylabs.AuthKey(apiURL, "work"), adversarylabs.Auth{Token: "test-token"}); err != nil {
		t.Fatal(err)
	}
	api := &runLifecycleAPI{calls: make(chan adversarylabs.RunUsageReport, 10)}
	deps.API = pullMetricAPIFactory{identity: store.Path, client: api}
	app, err := application.New(deps)
	if err != nil {
		t.Fatal(err)
	}
	ctx, flush := withRunUsageFinalizers(modelreview.WithUsageCollector(t.Context()))
	defer flush()
	finish := beginRunUsage(ctx, app, apiURL, "work", adversarylabs.RunUsageReport{Adversaries: []string{"local"}})
	<-api.calls
	finish(adversarylabs.RunUsageReport{Outcome: "completed"})
	if len(api.calls) != 0 {
		t.Fatal("run finalized before post-review work")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"output":[{"content":[{"type":"output_text","text":"{}"}]}],"usage":{"input_tokens":23,"output_tokens":4}}`)
	}))
	defer server.Close()
	provider := &modelreview.OpenAIProvider{ModelID: "post-review-model", BaseURL: server.URL, Client: server.Client()}
	if _, err := provider.Review(ctx, modelreview.Request{Schema: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	flush()
	wait, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	app.WaitBackground(wait)
	select {
	case report := <-api.calls:
		if len(report.ModelUsage) != 1 || report.ModelUsage[0].Model != "post-review-model" || *report.ModelUsage[0].InputTokens != 23 {
			t.Fatalf("report=%+v", report)
		}
	case <-wait.Done():
		t.Fatal("run did not finalize")
	}
}
