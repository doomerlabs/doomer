package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/doomerlabs/doomer/internal/application"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
)

type reviewStatusClient interface {
	GetReview(context.Context, string, string) (adversarylabs.ReviewStatus, error)
}

func waitForReview(ctx context.Context, clock application.Clock, client reviewStatusClient, token string, accepted adversarylabs.ReviewSubmission, interval time.Duration) (adversarylabs.ReviewStatus, error) {
	last := adversarylabs.ReviewStatus{ReviewSubmission: accepted}
	for {
		if err := ctx.Err(); err != nil {
			return last, fmt.Errorf("waiting for review %s: %w; resume with doomer review get %s", accepted.ID, err, accepted.ID)
		}
		current, err := client.GetReview(ctx, token, accepted.ID)
		if err != nil {
			return last, fmt.Errorf("waiting for review %s: %w; resume with doomer review get %s", accepted.ID, err, accepted.ID)
		}
		last = current
		switch current.Status {
		case "completed":
			return current, nil
		case "failed", "cancelled", "expired":
			return current, fmt.Errorf("review %s %s", current.ID, current.Status)
		}
		timer := clock.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return last, fmt.Errorf("waiting for review %s: %w; resume with doomer review get %s", accepted.ID, ctx.Err(), accepted.ID)
		case <-timer.C():
			timer.Stop()
		}
	}
}
