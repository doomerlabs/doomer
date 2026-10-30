package cmd

import (
	"context"
	"testing"
	"time"

	"github.com/doomerlabs/doomer/pkg/adversarylabs"
)

func TestReviewWaitPermutations(t *testing.T) {
	for _, enabled := range []struct{ look, full bool }{{true, true}, {true, false}, {false, true}, {false, false}} {
		for _, mode := range []string{"none", "look-closer", "deep", "all"} {
			got, err := resolveReviewWait(mode, true, enabled.look, enabled.full)
			valid := (enabled.look || enabled.full) && (mode != "look-closer" || enabled.look) && (mode != "deep" || enabled.full)
			if (err == nil) != valid {
				t.Fatalf("look=%t full=%t wait=%s: %v", enabled.look, enabled.full, mode, err)
			}
			if valid && (got.lookCloser != (enabled.look && (mode == "all" || mode == "look-closer")) || got.fullReview != (enabled.full && (mode == "all" || mode == "deep"))) {
				t.Fatalf("wrong wait policy: %+v", got)
			}
		}
		got, err := resolveReviewWait("look-closer", false, enabled.look, enabled.full)
		if enabled.look || enabled.full {
			if err != nil || got.lookCloser != enabled.look || got.fullReview {
				t.Fatalf("wrong default: %+v %v", got, err)
			}
		} else if err == nil {
			t.Fatal("accepted no enabled stages")
		}
	}
	if _, err := resolveReviewWait("unknown", true, true, true); err == nil {
		t.Fatal("accepted unknown wait mode")
	}
}

type stagedWorkspaceClient struct {
	states         []adversarylabs.WorkspaceReview
	polls, results int
}

func (c *stagedWorkspaceClient) GetWorkspace(context.Context, string, string) (adversarylabs.WorkspaceReview, error) {
	result := c.states[c.polls]
	c.polls++
	return result, nil
}
func (c *stagedWorkspaceClient) GetWorkspaceResults(context.Context, string, string) (adversarylabs.WorkspaceReview, error) {
	c.results++
	return c.states[len(c.states)-1], nil
}

func TestWorkspaceWaitsOnlyForSelectedStages(t *testing.T) {
	for _, tc := range []struct {
		mode           string
		polls, results int
	}{{"none", 0, 0}, {"look-closer", 1, 0}, {"deep", 2, 1}, {"all", 2, 1}} {
		t.Run(tc.mode, func(t *testing.T) {
			policy, err := resolveReviewWait(tc.mode, true, true, true)
			if err != nil {
				t.Fatal(err)
			}
			client := &stagedWorkspaceClient{states: []adversarylabs.WorkspaceReview{
				{ID: "review", Status: "running", Early: &adversarylabs.WorkspaceAssessment{Status: "ready"}},
				{ID: "review", Status: "completed", Early: &adversarylabs.WorkspaceAssessment{Status: "ready"}},
			}}
			_, err = waitForWorkspaceReview(context.Background(), &reviewTestClock{tick: true}, client, "token", adversarylabs.WorkspaceReview{ID: "review", Status: "queued"}, policy, time.Second)
			if err != nil || client.polls != tc.polls || client.results != tc.results {
				t.Fatalf("polls=%d results=%d error=%v", client.polls, client.results, err)
			}
		})
	}
}

func TestWaitingForFullDoesNotRequireLookCloserSuccess(t *testing.T) {
	for _, quick := range []string{"running", "unavailable"} {
		for _, mode := range []string{"deep", "all"} {
			policy, _ := resolveReviewWait(mode, true, true, true)
			state := adversarylabs.WorkspaceReview{ID: "review", Status: "completed", Early: &adversarylabs.WorkspaceAssessment{Status: quick}}
			client := &stagedWorkspaceClient{states: []adversarylabs.WorkspaceReview{state}}
			_, err := waitForWorkspaceReview(context.Background(), &reviewTestClock{tick: true}, client, "token", state, policy, time.Second)
			if (err == nil) != (mode == "deep") {
				t.Fatalf("quick=%s wait=%s err=%v", quick, mode, err)
			}
		}
	}
}
