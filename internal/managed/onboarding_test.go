package managed

import (
	"context"
	"github.com/doomerlabs/doomer/pkg/sourcesnapshot"
	"strings"
	"testing"
)

func TestSubmitWaitsForAcceptedOnboarding(t *testing.T) {
	e := Engine{Capture: func(context.Context, Task) (sourcesnapshot.Capture, error) {
		t.Fatal("captured source before accepted setup finished")
		return sourcesnapshot.Capture{}, nil
	}}
	task := Task{OnboardingPending: true, Stage: "ready"}
	if err := e.Submit(context.Background(), &task, ""); err == nil || !strings.Contains(err.Error(), "onboarding is incomplete") {
		t.Fatal(err)
	}
}
