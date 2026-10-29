package watch

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// The agent only prints the handoff. The trusted hook records it, bound to the
// exact profile, conversation, and turn that received the numbered leads.
type LeadResponse struct {
	Number int    `json:"number"`
	Status string `json:"status"`
	Agrees *bool  `json:"agrees"`
	Reason string `json:"reason"`
}
type responseEnvelope struct {
	Session string         `json:"session"`
	Request string         `json:"request"`
	Items   []LeadResponse `json:"items"`
}

func responseInstructions(root string, cfg Config, turn Turn) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	command := Quote(exe) + " --profile " + Quote(cfg.Profile) + " --api-url " + Quote(cfg.API) + " watch respond --state " + Quote(root) + " --session " + Quote(turn.Owner.Session) + " --request " + Quote(turn.ResponseToken)
	return fmt.Sprintf(`
Investigate every supplied item, including suspected false positives. Before your final reply, record each decision through this local command by passing a JSON array on stdin:
%s
Each item is {"number":1,"status":"fixed|dismissed|deferred","agrees":true,"reason":"concrete evidence and checks"}. Use the actual supplied number. Use agrees=false for a disproved concern, null only when unassessed. Use fixed only after an authorized fix is verified, dismissed for disproved concerns, and deferred for valid out-of-scope or unassessed concerns. Submit every decision, including dismissals. Chat prose alone does not record a response.
This helper only reads the local request and prints an envelope; it does not access credentials, write files, or contact any server. The installed hook captures it and the configured watcher uploads the responses. A pending_hook result is a local handoff, not confirmation of server delivery. If unavailable or denied, continue without repeated retries; never claim a response was recorded without acknowledgement.
For the user, summarize the meaningful outcome in one or two natural sentences naming Doomer.ai. Mention confirmed issues you fixed and any actionable concerns that remain. Count only verified fixes as fixed; distinguish agreement from a fix. Do not list numbered reports, repeat every concern, or mention dismissed false positives and noise unless the user asks. If all concerns were dismissed, omit the Doomer recap. Generate wording suited to what actually happened; do not copy a canned sentence. Include the review link once when useful. Do not wait for the full review.
`, command), nil
}
func validateResponses(turn Turn, request string, items []LeadResponse) error {
	if request == "" || turn.ResponseToken != request || len(items) == 0 || len(items) > 128 {
		return errors.New("response request unavailable")
	}
	pending := map[int]bool{}
	for _, p := range turn.Pending {
		pending[p.Number] = true
	}
	for _, item := range items {
		if !pending[item.Number] || strings.TrimSpace(item.Reason) == "" || len(item.Reason) > 2000 {
			return errors.New("invalid lead response")
		}
		delete(pending, item.Number)
		switch item.Status {
		case "fixed":
			if item.Agrees == nil || !*item.Agrees {
				return errors.New("fixed response requires agreement")
			}
		case "dismissed":
			if item.Agrees == nil || *item.Agrees {
				return errors.New("dismissed response requires disagreement")
			}
		case "deferred":
		default:
			return errors.New("invalid response status")
		}
	}
	return nil
}
func EmitResponses(root, session, request string, input io.Reader, out io.Writer) error {
	var turn Turn
	if Read(TurnPath(root, session), &turn) != nil || turn.Owner.Session != session {
		return errors.New("response request unavailable")
	}
	var items []LeadResponse
	dec := json.NewDecoder(io.LimitReader(input, 256<<10))
	dec.DisallowUnknownFields()
	if dec.Decode(&items) != nil || dec.Decode(new(any)) != io.EOF {
		return errors.New("invalid lead responses")
	}
	if err := validateResponses(turn, request, items); err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(map[string]any{"pending_hook": true, "doomer_responses": responseEnvelope{session, request, items}})
}
func recordResponsesOutput(root string, owner Owner, raw json.RawMessage) int {
	if len(raw) > 256<<10 {
		return 0
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return 0
	}
	payload := responsePayload(value, 0)
	var envelope responseEnvelope
	if payload == nil || json.Unmarshal(payload, &envelope) != nil || envelope.Session != owner.Session {
		return 0
	}
	recorded := 0
	_ = Locked(root, owner.Session, func() error {
		var turn Turn
		if Read(TurnPath(root, owner.Session), &turn) != nil || turn.Owner != owner {
			return nil
		}
		if err := validateResponses(turn, envelope.Request, envelope.Items); err != nil {
			return err
		}
		items := map[int]LeadResponse{}
		for _, item := range envelope.Items {
			items[item.Number] = item
		}
		keep := []Pending{}
		for _, pending := range turn.Pending {
			item, ok := items[pending.Number]
			if !ok {
				keep = append(keep, pending)
				continue
			}
			report := Feedback{pending.Lead.ID, pending.ReviewID, item.Status, strings.TrimSpace(item.Reason), item.Agrees}
			if err := Write(filepath.Join(root, "feedback", Key(pending.ReviewID+pending.Lead.ID)+".json"), report); err != nil {
				return err
			}
		}
		turn.Pending = keep
		if err := Write(TurnPath(root, owner.Session), turn); err != nil {
			return err
		}
		recorded = len(envelope.Items)
		return nil
	})
	return recorded
}
func responsePayload(value any, depth int) json.RawMessage {
	if depth > 6 {
		return nil
	}
	switch v := value.(type) {
	case map[string]any:
		if payload, ok := v["doomer_responses"]; ok {
			b, _ := json.Marshal(payload)
			return b
		}
		for _, key := range []string{"output", "stdout", "content", "text", "result", "aggregated_output"} {
			if found := responsePayload(v[key], depth+1); found != nil {
				return found
			}
		}
	case []any:
		for _, child := range v {
			if found := responsePayload(child, depth+1); found != nil {
				return found
			}
		}
	case string:
		if len(v) > 256<<10 {
			return nil
		}
		var decoded any
		if json.Unmarshal([]byte(v), &decoded) == nil {
			return responsePayload(decoded, depth+1)
		}
		for _, line := range strings.Split(v, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "{") && json.Unmarshal([]byte(line), &decoded) == nil {
				if found := responsePayload(decoded, depth+1); found != nil {
					return found
				}
			}
		}
	}
	return nil
}
