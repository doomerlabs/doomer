// Package livewatch watches repositories and submits settled changes.
// Runtime state stays in memory; the caller owns the process lifetime.
package livewatch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"github.com/doomerlabs/doomer/pkg/sourcesnapshot"
)

func Key(value string) string { sum := sha256.Sum256([]byte(value)); return hex.EncodeToString(sum[:]) }

// Config describes one watch invocation. Its caller controls when it stops.
type ConsentEvidence struct {
	Account             string   `json:"account"`
	Service             string   `json:"service"`
	Profile             string   `json:"profile"`
	CredentialDirectory string   `json:"credential_directory"`
	Agent               string   `json:"agent"`
	AllRepositories     bool     `json:"all_repositories"`
	Repositories        []string `json:"repositories,omitempty"`
	Includes            []string `json:"includes"`
}

type Config struct {
	lifecycle    *reviewLifecycle
	editBaseline map[string]initialFile
	assessment   *HypothesisResponse

	RequireHypotheses bool `json:"require_hypotheses,omitempty"`
	hypotheses        func(context.Context, string, string, int) ([]adversarylabs.WorkspaceHypothesis, error)

	Consent           func() *ConsentEvidence             `json:"-"`
	ID                string                              `json:"id"`
	ControlKey        string                              `json:"control_key,omitempty"`
	Directory         string                              `json:"directory"`
	API               string                              `json:"api"`
	Token             string                              `json:"token"`
	Request           *adversarylabs.WorkspaceUserRequest `json:"request,omitempty"`
	Agent             string                              `json:"agent,omitempty"`
	DrainTimeout      time.Duration                       `json:"drain_timeout,omitempty"`
	Stop              <-chan struct{}                     `json:"-"`
	NoticesReady      <-chan struct{}                     `json:"-"`
	Notices           func() []adversarylabs.ClientNotice `json:"-"`
	AcknowledgeNotice func(string, int) error             `json:"-"`
	Report            func(Result)                        `json:"-"`
}

type Client interface {
	UploadWorkspaceTree(context.Context, string, string, []sourcesnapshot.Entry, map[string][]byte) (string, string, error)
	StartWorkspace(context.Context, string, adversarylabs.WorkspaceSubmission) (adversarylabs.WorkspaceReview, error)
	GetWorkspace(context.Context, string, string) (adversarylabs.WorkspaceReview, error)
	ReportWorkspaceLead(context.Context, string, string, string, string, string, *bool) error
}

type Result struct {
	Repository string                        `json:"repository"`
	ID         string                        `json:"id,omitempty"`
	URL        string                        `json:"url,omitempty"`
	Status     string                        `json:"status"`
	Leads      []adversarylabs.WorkspaceLead `json:"leads,omitempty"`
	Error      string                        `json:"error,omitempty"`
}

type Feedback struct {
	ReviewID string `json:"review_id"`
	LeadID   string `json:"lead_id"`
	Status   string `json:"status"`
	Reason   string `json:"reason"`
	Agrees   *bool  `json:"agrees"`
}

type Finding struct {
	Repository  string                      `json:"repository"`
	ReviewID    string                      `json:"review_id"`
	URL         string                      `json:"url,omitempty"`
	Lead        adversarylabs.WorkspaceLead `json:"lead"`
	Disposition *Feedback                   `json:"disposition,omitempty"`
}

type Response struct {
	ReviewPending       bool                `json:"review_pending,omitempty"`
	OtherReviewPending  bool                `json:"other_review_pending,omitempty"`
	HypothesisRequests  []HypothesisRequest `json:"hypothesis_requests,omitempty"`
	RepeatedStopHandoff bool                `json:"repeated_stop_handoff,omitempty"`

	Consent   *ConsentEvidence             `json:"consent,omitempty"`
	Ready     bool                         `json:"ready"`
	API       string                       `json:"api,omitempty"`
	Directory string                       `json:"directory,omitempty"`
	Results   []Result                     `json:"results,omitempty"`
	Findings  []Finding                    `json:"findings,omitempty"`
	Notices   []adversarylabs.ClientNotice `json:"notices,omitempty"`
	Error     string                       `json:"error,omitempty"`
}

type request struct {
	Hypotheses       *HypothesisResponse `json:"hypotheses,omitempty"`
	AssessmentID     string              `json:"assessment_id,omitempty"`
	AssessmentDenial AssessmentDenial    `json:"assessment_denial,omitempty"`

	Key            string     `json:"key"`
	Operation      string     `json:"operation"`
	Directory      string     `json:"directory,omitempty"`
	Feedback       []Feedback `json:"feedback,omitempty"`
	NoticeID       string     `json:"notice_id,omitempty"`
	NoticeRevision int        `json:"notice_revision,omitempty"`
}
