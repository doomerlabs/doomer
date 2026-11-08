package adversarylabs

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/doomerlabs/doomer/internal/version"
)

const NoticeSchema = 1

type ClientNotice struct {
	NotifyUser         bool       `json:"notify_user"`
	ID                 string     `json:"id"`
	Revision           int        `json:"revision"`
	Component          string     `json:"component"`
	Severity           string     `json:"severity"`
	Message            string     `json:"message"`
	RecommendedVersion string     `json:"recommended_version"`
	ExpiresAt          time.Time  `json:"expires_at"`
	Deadline           *time.Time `json:"deadline,omitempty"`
	Blocking           bool       `json:"blocking"`
	Cached             bool       `json:"cached,omitempty"`
}
type NoticeEnvelope struct {
	Schema  int            `json:"notice_schema"`
	Notices []ClientNotice `json:"notices"`
}

var noticeID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
var noticeVersion = regexp.MustCompile(`^20[0-9]{2}\.[1-9][0-9]?\.[1-9][0-9]?\.[1-9][0-9]{0,5}$`)

func ValidNotices(envelope NoticeEnvelope, now time.Time) []ClientNotice {
	if envelope.Schema != NoticeSchema {
		return nil
	}
	var result []ClientNotice
	for _, n := range envelope.Notices {
		if len(result) == 4 {
			break
		}
		if !noticeID.MatchString(n.ID) || n.Revision < 1 || n.Component != "cli" || !noticeVersion.MatchString(n.RecommendedVersion) || !now.Before(n.ExpiresAt) || len(n.Message) == 0 || len(n.Message) > 1600 {
			continue
		}
		switch n.Severity {
		case "available", "recommended", "critical", "deprecated":
		default:
			continue
		}
		// Plain text only: never interpret server text as commands or terminal control.
		n.Message = strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return ' '
			}
			return r
		}, n.Message)
		result = append(result, n)
	}
	return result
}

type noticeContextKey struct{}
type noticeCache struct {
	Version string               `json:"version"`
	Notices []ClientNotice       `json:"notices"`
	Shown   map[string]time.Time `json:"shown"`
}

// NoticeCollector belongs to one command. It never grants update authorization
// or blocks offline work using cached policy; only the server enforces support.
type NoticeCollector struct {
	mu      sync.Mutex
	path    string
	writer  io.Writer
	notices []ClientNotice
	shown   map[string]time.Time
}

func WithNoticeCollector(ctx context.Context, configDir, api string, writer io.Writer) context.Context {
	sum := sha256.Sum256([]byte(api))
	c := &NoticeCollector{path: filepath.Join(configDir, "notices", hex.EncodeToString(sum[:])+".json"), writer: writer, shown: map[string]time.Time{}}
	var cache noticeCache
	if raw, err := os.ReadFile(c.path); err == nil && len(raw) <= 32*1024 && json.Unmarshal(raw, &cache) == nil && cache.Version == version.Version {
		c.notices = ValidNotices(NoticeEnvelope{Schema: 1, Notices: cache.Notices}, time.Now())
		if cache.Shown != nil {
			c.shown = cache.Shown
		}
		for i := range c.notices {
			c.notices[i].Cached = true
		}
	}
	return context.WithValue(ctx, noticeContextKey{}, c)
}
func NoticesFromContext(ctx context.Context) []ClientNotice {
	c, _ := ctx.Value(noticeContextKey{}).(*NoticeCollector)
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	result := append([]ClientNotice(nil), ValidNotices(NoticeEnvelope{Schema: 1, Notices: c.notices}, time.Now())...)
	for i := range result {
		result[i].NotifyUser = c.shouldShow(result[i])
	}
	return result
}
func (c *NoticeCollector) receive(envelope NoticeEnvelope, token string) {
	if envelope.Schema != NoticeSchema {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.notices = ValidNotices(envelope, time.Now())
	for i := range c.notices {
		n := &c.notices[i]
		n.Cached = false
		if token != "" {
			n.Message = strings.ReplaceAll(n.Message, token, "[redacted]")
		}
	}
	c.persist()
}
func (c *NoticeCollector) persist() {
	if len(c.notices) == 0 {
		if _, err := os.Stat(c.path); os.IsNotExist(err) {
			return
		}
	}
	if os.MkdirAll(filepath.Dir(c.path), 0700) != nil {
		return
	}
	raw, err := json.Marshal(noticeCache{Version: version.Version, Notices: c.notices, Shown: c.shown})
	if err != nil {
		return
	}
	f, err := os.CreateTemp(filepath.Dir(c.path), ".notices-*")
	if err != nil {
		return
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(raw); err != nil {
		f.Close()
		return
	}
	if f.Close() != nil {
		return
	}
	_ = os.Rename(name, c.path)
}

// Flush after command output so background workers can persist without consuming
// the user's one-time foreground warning. JSON always retains current notices.
func FlushNotices(ctx context.Context) {
	c, _ := ctx.Value(noticeContextKey{}).(*NoticeCollector)
	if c == nil || c.writer == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, n := range ValidNotices(NoticeEnvelope{Schema: 1, Notices: c.notices}, time.Now()) {
		if !c.shouldShow(n) {
			continue
		}
		key := fmt.Sprintf("%s:%d", n.ID, n.Revision)
		cached := ""
		if n.Cached {
			cached = " (last known notice; check online)"
		}
		fmt.Fprintf(c.writer, "Doomer %s%s: %s Recommended version: %s. Update information: https://doomer.ai/updates\n", n.Severity, cached, n.Message, n.RecommendedVersion)
		c.shown[key] = time.Now()
	}
	c.persist()
}

func (c *NoticeCollector) shouldShow(n ClientNotice) bool {
	if n.Severity == "available" {
		return false
	}
	last := c.shown[fmt.Sprintf("%s:%d", n.ID, n.Revision)]
	repeat := 24 * time.Hour
	if n.Severity == "recommended" {
		repeat = 7 * 24 * time.Hour
	}
	return n.Blocking || last.IsZero() || time.Since(last) >= repeat
}
func DeferNotice(ctx context.Context, id string) error {
	c, _ := ctx.Value(noticeContextKey{}).(*NoticeCollector)
	if c == nil {
		return fmt.Errorf("notice context unavailable")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, n := range c.notices {
		if n.ID != id {
			continue
		}
		if n.Blocking || n.Severity == "critical" || n.Severity == "deprecated" {
			return fmt.Errorf("critical and deprecation notices cannot be deferred")
		}
		c.shown[fmt.Sprintf("%s:%d", n.ID, n.Revision)] = time.Now()
		c.persist()
		return nil
	}
	return fmt.Errorf("notice not found")
}

type noticeTransport struct {
	base   http.RoundTripper
	origin string
}

func (t noticeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	origin, _ := url.Parse(t.origin)
	if origin == nil || !strings.EqualFold(req.URL.Host, origin.Host) || req.URL.Scheme != origin.Scheme || !(req.URL.Path == strings.TrimRight(origin.Path, "/") || strings.HasPrefix(req.URL.Path, strings.TrimRight(origin.Path, "/")+"/")) {
		return t.base.RoundTrip(req)
	}
	clone := req.Clone(req.Context())
	clone.Header = req.Header.Clone()
	clone.Header.Set("User-Agent", "doomer/"+version.Version)
	clone.Header.Set("X-Doomer-CLI-Version", version.Version)
	clone.Header.Set("X-Doomer-Protocol", "2")
	clone.Header.Set("X-Doomer-Notice-Schema", "1")
	clone.Header.Set("X-Doomer-Platform", runtime.GOOS+"/"+runtime.GOARCH)
	response, err := t.base.RoundTrip(clone)
	if err != nil {
		return response, err
	}
	header := response.Header.Get("X-Doomer-Notices")
	if len(header) > 0 && len(header) <= 12*1024 {
		if raw, err := base64.StdEncoding.DecodeString(header); err == nil {
			var envelope NoticeEnvelope
			if json.Unmarshal(raw, &envelope) == nil {
				if c, _ := req.Context().Value(noticeContextKey{}).(*NoticeCollector); c != nil {
					c.receive(envelope, strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer "))
				}
			}
		}
	}
	return response, nil
}
func (c Client) CheckNotices(ctx context.Context) (NoticeEnvelope, error) {
	var out NoticeEnvelope
	if _, err := validateBaseURL(c.BaseURL); err != nil {
		return out, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/v1/cli/notices", nil)
	if err != nil {
		return out, err
	}
	response, err := c.httpClient().Do(req)
	if err != nil {
		return out, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return out, responseError(response, "")
	}
	err = decodeLimited(response.Body, &out)
	if err == nil {
		out.Notices = ValidNotices(out, time.Now())
		if collector, _ := ctx.Value(noticeContextKey{}).(*NoticeCollector); collector != nil {
			collector.receive(out, "")
		}
	}
	return out, err
}
