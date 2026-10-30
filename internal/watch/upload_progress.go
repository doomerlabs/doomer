package watch

import "time"

// Report cumulative transfer progress across both snapshots. The compressed
// total is not known until all packs are prepared, so do not invent a percentage.
type uploadProgressDisplay struct {
	log         func(string, ...any)
	label       string
	now         func() time.Time
	lastReport  time.Time
	sent, total int
	reported    int
	finished    bool
}

func (d *uploadProgressDisplay) update(stage string, _ int, bytes int, _ time.Duration) {
	now := time.Now()
	if d.now != nil {
		now = d.now()
	}
	if d.lastReport.IsZero() {
		d.lastReport = now
	}
	current := 0
	switch stage {
	case "transfer_total":
		d.total = bytes
		d.finished = false
		return
	case "transfer":
		if d.finished {
			return
		}
		current = max(0, min(bytes, d.total))
	case "transfer_done":
		if d.finished {
			return
		}
		d.sent += max(0, min(bytes, d.total))
		d.finished = true
	default:
		return
	}
	if now.Sub(d.lastReport) < 5*time.Second || d.sent+current == 0 {
		return
	}
	// Pack retries restart their byte counter. Keep the display monotonic
	// without counting failed attempts as additional completed content.
	d.reported = max(d.reported, d.sent+current)
	d.log("%s: sending contents · %s sent so far", d.label, formatUploadBytes(d.reported))
	d.lastReport = now
}
