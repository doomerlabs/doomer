package githubreview

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/doomerlabs/doomer/internal/githubapi"
)

func TestPostDryRunNoop(t *testing.T) {
	res, err := Post(context.Background(), CommentPlan{Comments: []PlannedComment{{FindingID: "x"}}}, PostOptions{DryRun: true})
	if err != nil || res == nil {
		t.Fatalf("%v %#v", err, res)
	}
}

func TestPostReplyOnlyDoesNotCreateReview(t *testing.T) {
	var reviewCreated bool
	var replyBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		switch {
		case strings.Contains(payload.Query, "addPullRequestReviewThreadReply"):
			input := payload.Variables["input"].(map[string]any)
			replyBody = input["body"].(string)
			_, _ = w.Write([]byte(`{"data":{"addPullRequestReviewThreadReply":{"comment":{"id":"C1"}}}}`))
		case strings.Contains(payload.Query, "addPullRequestReview"):
			reviewCreated = true
			w.WriteHeader(http.StatusBadRequest)
		default:
			_, _ = w.Write([]byte(`{"data":{"repository":{"pullRequest":{"id":"PR1","headRefOid":"abc"}}}}`))
		}
	}))
	defer server.Close()
	client := githubapi.NewClient("token")
	client.HTTP = server.Client()
	client.GQLURL = server.URL
	result, err := Post(context.Background(), CommentPlan{HeadSHA: "abc", Replies: []ThreadReply{{ThreadID: "T1", Comment: PlannedComment{Adversary: "review/code", FindingID: "f1", Summary: "The write lacks authorization.", Recommendation: "Call authorize first."}}}}, PostOptions{Client: client, Owner: "o", Repo: "r", Number: 1})
	if err != nil || result.Replied != 1 || reviewCreated || !strings.Contains(replyBody, "Suggested fix: Call authorize first.") || !strings.Contains(replyBody, "adversary-review:v2") {
		t.Fatalf("result=%+v err=%v reviewCreated=%v replyBody=%q", result, err, reviewCreated, replyBody)
	}
}

func TestPostReviewBasisOnly(t *testing.T) {
	var addInput map[string]any
	filesCalls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/files") {
			filesCalls++
			_, _ = w.Write([]byte(`[]`))
			return
		}
		var payload struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		switch {
		case strings.Contains(payload.Query, "pullRequest(number"):
			_, _ = w.Write([]byte(`{"data":{"repository":{"pullRequest":{"id":"PR_1","headRefOid":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","url":"https://github.com/o/r/pull/1"}}}}`))
		case strings.Contains(payload.Query, "addPullRequestReview"):
			addInput, _ = payload.Variables["input"].(map[string]any)
			_, _ = w.Write([]byte(`{"data":{"addPullRequestReview":{"pullRequestReview":{"id":"RV_1","url":"https://github.com/o/r/pull/1#pullrequestreview-1","state":"PENDING"}}}}`))
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := githubapi.NewClient("t")
	client.HTTP = srv.Client()
	client.RESTBase = srv.URL
	client.GQLURL = srv.URL + "/"
	var msgs []string
	res, err := Post(context.Background(), CommentPlan{ReviewBasis: "Reviewed as: inferred outcome."}, PostOptions{
		Client: client,
		Owner:  "o", Repo: "r", Number: 1,
		Progress: func(s string) { msgs = append(msgs, s) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if res == nil {
		t.Fatal("nil result")
	}
	if res.ReviewID != "RV_1" {
		t.Fatalf("result = %#v", res)
	}
	body, _ := addInput["body"].(string)
	if !strings.Contains(body, "Reviewed as: inferred outcome.") {
		t.Fatalf("review body = %q", body)
	}
	if len(msgs) == 0 || strings.Contains(msgs[0], "nothing to post") {
		t.Fatalf("progress = %v", msgs)
	}
	if filesCalls != 0 {
		t.Fatalf("basis-only review fetched pull request files %d time(s)", filesCalls)
	}
}

func TestPostEscapesReviewBasisMarkdown(t *testing.T) {
	var addInput map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/files") {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		var payload struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		switch {
		case strings.Contains(payload.Query, "pullRequest(number"):
			_, _ = w.Write([]byte(`{"data":{"repository":{"pullRequest":{"id":"PR_1","headRefOid":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","url":"https://github.com/o/r/pull/1"}}}}`))
		case strings.Contains(payload.Query, "addPullRequestReview"):
			addInput, _ = payload.Variables["input"].(map[string]any)
			_, _ = w.Write([]byte(`{"data":{"addPullRequestReview":{"pullRequestReview":{"id":"RV_1","url":"https://github.com/o/r/pull/1#pullrequestreview-1","state":"PENDING"}}}}`))
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := githubapi.NewClient("t")
	client.HTTP = srv.Client()
	client.RESTBase = srv.URL
	client.GQLURL = srv.URL + "/"
	_, err := Post(context.Background(), CommentPlan{
		ReviewBasis: "Reviewed as: [click](https://evil.example) <img src=x> **trusted**\n\n> quote\n# heading\n---",
	}, PostOptions{Client: client, Owner: "o", Repo: "r", Number: 1})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := addInput["body"].(string)
	if strings.Contains(body, "[click](https://evil.example)") || strings.Contains(body, "<img") {
		t.Fatalf("review body contains active markdown: %q", body)
	}
	if !strings.Contains(body, `\[click\]\(https://evil.example\) &lt;img src=x&gt; \*\*trusted\*\*`) {
		t.Fatalf("review body did not preserve escaped text: %q", body)
	}
	for _, marker := range []string{"\n> quote", "\n# heading", "\n---"} {
		if strings.Contains(body, marker) {
			t.Fatalf("review body contains injected block marker %q: %q", marker, body)
		}
	}
}

func TestPostCreatesPendingReview(t *testing.T) {
	var gqlBodies []string
	var addInput map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// GraphQL endpoint is absolute URL set on client
		if r.Method == http.MethodPost {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			q, _ := body["query"].(string)
			gqlBodies = append(gqlBodies, q)
			if strings.Contains(q, "pullRequest(number") {
				_, _ = w.Write([]byte(`{"data":{"repository":{"pullRequest":{"id":"PR_1","headRefOid":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","url":"https://github.com/o/r/pull/1"}}}}`))
				return
			}
			if strings.Contains(q, "addPullRequestReview") {
				variables, _ := body["variables"].(map[string]any)
				addInput, _ = variables["input"].(map[string]any)
				_, _ = w.Write([]byte(`{"data":{"addPullRequestReview":{"pullRequestReview":{"id":"RV_1","url":"https://github.com/o/r/pull/1#pullrequestreview-1","state":"PENDING"}}}}`))
				return
			}
			if strings.Contains(q, "submitPullRequestReview") {
				_, _ = w.Write([]byte(`{"data":{"submitPullRequestReview":{"pullRequestReview":{"id":"RV_1","url":"https://github.com/o/r/pull/1#pullrequestreview-1","state":"COMMENTED"}}}}`))
				return
			}
		}
		if strings.Contains(r.URL.Path, "/files") {
			_, _ = w.Write([]byte(`[{"filename":"a.go","patch":"@@ -1,1 +1,2 @@\n keep\n+added\n"}]`))
			return
		}
		w.WriteHeader(404)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := githubapi.NewClient("tok")
	c.HTTP = srv.Client()
	c.RESTBase = srv.URL
	c.GQLURL = srv.URL + "/"

	line := 2
	plan := CommentPlan{
		ReviewBody:  "overall",
		ReviewBasis: "Reviewed as: preserve pull-only registry access.",
		Comments: []PlannedComment{{
			FindingID: "f1", Title: "T", Severity: "high", Body: "body text", BodySource: "template",
			Placement: "inline", Anchor: Anchor{Path: "a.go", Line: &line},
		}},
	}
	res, err := Post(context.Background(), plan, PostOptions{
		Client: c, Owner: "o", Repo: "r", Number: 1, Submit: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ReviewID != "RV_1" || res.State != "COMMENTED" {
		t.Fatalf("%#v gql=%v", res, gqlBodies)
	}
	if res.Posted != 1 || len(res.PostedComments) != 1 || res.PostedComments[0].FindingID != "f1" {
		t.Fatalf("posted comments = %#v", res.PostedComments)
	}
	body, _ := addInput["body"].(string)
	if !strings.Contains(body, "Reviewed as: preserve pull-only registry access.") {
		t.Fatalf("review body = %q", body)
	}
}

func TestPostInlineOnlyReviewOmitsBody(t *testing.T) {
	var addInput map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/files") {
			_, _ = w.Write([]byte(`[{"filename":"a.go","patch":"@@ -1,1 +1,2 @@\n keep\n+added\n"}]`))
			return
		}
		var payload struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		switch {
		case strings.Contains(payload.Query, "pullRequest(number"):
			_, _ = w.Write([]byte(`{"data":{"repository":{"pullRequest":{"id":"PR_1","headRefOid":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","url":"https://github.com/o/r/pull/1"}}}}`))
		case strings.Contains(payload.Query, "addPullRequestReview"):
			addInput, _ = payload.Variables["input"].(map[string]any)
			_, _ = w.Write([]byte(`{"data":{"addPullRequestReview":{"pullRequestReview":{"id":"RV_1","url":"https://github.com/o/r/pull/1#pullrequestreview-1","state":"PENDING"}}}}`))
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := githubapi.NewClient("tok")
	client.HTTP = srv.Client()
	client.RESTBase = srv.URL
	client.GQLURL = srv.URL + "/"
	line := 2
	_, err := Post(context.Background(), CommentPlan{Comments: []PlannedComment{{
		FindingID: "f", Title: "Finding", Severity: "high", Body: "inline body",
		Placement: "inline", Anchor: Anchor{Path: "a.go", Line: &line},
	}}}, PostOptions{Client: client, Owner: "o", Repo: "r", Number: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := addInput["body"]; ok {
		t.Fatalf("inline-only review unexpectedly included body: %#v", addInput)
	}
}

func TestPostFoldedCommentsKeepOnlyCommentText(t *testing.T) {
	for _, tc := range []struct {
		name, path    string
		rejectThreads bool
	}{
		{name: "review body placement", path: "other.go"},
		{name: "API fallback", path: "a.go", rejectThreads: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var addInput map[string]any
			addCalls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/files") {
					_, _ = w.Write([]byte(`[{"filename":"a.go","patch":"@@ -1,1 +1,2 @@\n keep\n+added\n"}]`))
					return
				}
				var payload struct {
					Query     string         `json:"query"`
					Variables map[string]any `json:"variables"`
				}
				_ = json.NewDecoder(r.Body).Decode(&payload)
				switch {
				case strings.Contains(payload.Query, "pullRequest(number"):
					_, _ = w.Write([]byte(`{"data":{"repository":{"pullRequest":{"id":"PR_1","headRefOid":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}}}`))
				case strings.Contains(payload.Query, "addPullRequestReview"):
					addCalls++
					addInput, _ = payload.Variables["input"].(map[string]any)
					if tc.rejectThreads && addCalls == 1 {
						_, _ = w.Write([]byte(`{"errors":[{"message":"Field 'threads' is not defined"}]}`))
						return
					}
					_, _ = w.Write([]byte(`{"data":{"addPullRequestReview":{"pullRequestReview":{"id":"RV_1","state":"PENDING"}}}}`))
				default:
					w.WriteHeader(http.StatusBadRequest)
				}
			}))
			defer server.Close()

			client := githubapi.NewClient("tok")
			client.HTTP = server.Client()
			client.RESTBase = server.URL
			client.GQLURL = server.URL + "/"
			line := 2
			comment := "Fix the stale row.\n\n<!-- adversary-review:v1 adversary=review%2Fcode finding=f loc=a.go:2 -->"
			_, err := Post(context.Background(), CommentPlan{Comments: []PlannedComment{{
				FindingID: "f", Title: "Stale row", Severity: "high", Body: comment,
				Placement: "inline", Anchor: Anchor{Path: tc.path, Line: &line},
			}}}, PostOptions{Client: client, Owner: "o", Repo: "r", Number: 1})
			if err != nil {
				t.Fatal(err)
			}
			body, _ := addInput["body"].(string)
			if !strings.Contains(body, comment) || !strings.Contains(body, "adversary-review:v1 batch") {
				t.Fatalf("comment or marker missing from review body: %q", body)
			}
			visible := stripReviewMarker(body)
			for _, unwanted := range []string{"high", "Stale row", "**a.go**", "_a.go:2_"} {
				if strings.Contains(visible, unwanted) {
					t.Fatalf("review body leaked %q: %q", unwanted, body)
				}
			}
			if tc.rejectThreads && addCalls != 2 {
				t.Fatalf("expected fallback review attempt, got %d calls", addCalls)
			}
		})
	}
}

func TestWritePlanFile(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/plan.json"
	if err := WritePlanFile(path, CommentPlan{SchemaVersion: 1, Source: "adversary.review.v1"}); err != nil {
		t.Fatal(err)
	}
}
