package adversarylabs

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/doomerlabs/doomer/pkg/sourcesnapshot"
)

type workspacePackEntry struct {
	SHA    string `json:"sha256"`
	Size   int    `json:"size"`
	Offset int    `json:"byte_offset"`
	Length int    `json:"length"`
}
type workspacePack struct {
	Data    []byte
	Entries []workspacePackEntry
}

func visitWorkspacePacks(files []workspaceMissing, blobs map[string][]byte, accept func(workspacePack) error) error {
	pack := workspacePack{}
	raw := 0
	for _, file := range files {
		data, ok := blobs[file.SHA]
		if !ok || len(data) != file.Size || sourcesnapshot.Digest(data) != file.SHA {
			return fmt.Errorf("pack source does not match manifest")
		}
		var encoded bytes.Buffer
		writer, _ := gzip.NewWriterLevel(&encoded, gzip.BestSpeed)
		if _, err := writer.Write(data); err != nil {
			return err
		}
		if err := writer.Close(); err != nil {
			return err
		}
		if len(pack.Entries) > 0 && (len(pack.Data)+encoded.Len() > 32<<20 || raw+len(data) > 64<<20 || len(pack.Entries) >= 4096) {
			if err := accept(pack); err != nil {
				return err
			}
			pack = workspacePack{}
			raw = 0
		}
		pack.Entries = append(pack.Entries, workspacePackEntry{SHA: file.SHA, Size: file.Size, Offset: len(pack.Data), Length: encoded.Len()})
		pack.Data = append(pack.Data, encoded.Bytes()...)
		raw += len(data)
	}
	if len(pack.Entries) > 0 {
		if err := accept(pack); err != nil {
			return err
		}
	}
	return nil
}
func (c Client) uploadWorkspacePack(ctx context.Context, token string, pack workspacePack, progress WorkspaceUploadProgress) (int, error) {
	var session struct {
		ID      string            `json:"id"`
		URL     string            `json:"upload_url"`
		Headers map[string]string `json:"headers"`
	}
	if err := c.postJSON(ctx, "/v1/repositories/packs", map[string]any{"action": "create", "sha256": sourcesnapshot.Digest(pack.Data), "compressed_size": len(pack.Data), "entries": pack.Entries}, token, &session); err != nil {
		return 0, err
	}
	u, err := url.Parse(session.URL)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || net.ParseIP(u.Hostname()).IsLoopback()))) {
		return 0, fmt.Errorf("invalid signed pack upload destination")
	}
	if !workspaceID(session.ID) {
		return 0, fmt.Errorf("invalid pack upload session")
	}
	client := *c.httpClient()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	started := time.Now()
	var transferred atomic.Int64
	if progress != nil {
		progress("transfer_total", len(pack.Entries), len(pack.Data), 0)
	}
	progressCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-progressCtx.Done():
				return
			case <-ticker.C:
				if progress != nil {
					progress("transfer", len(pack.Entries), min(len(pack.Data), int(transferred.Load())), time.Since(started))
				}
			}
		}
	}()
	defer func() { stop(); <-done }()
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			timer := time.NewTimer(time.Duration(attempt) * 300 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return 0, ctx.Err()
			case <-timer.C:
			}
			transferred.Store(0)
		}
		request, e := http.NewRequestWithContext(ctx, http.MethodPut, session.URL, bytes.NewReader(pack.Data))
		if e != nil {
			return 0, e
		}
		for name, value := range session.Headers {
			switch strings.ToLower(name) {
			case "content-length":
				if value != fmt.Sprint(len(pack.Data)) {
					return 0, fmt.Errorf("signed pack size mismatch")
				}
			case "content-type", "x-amz-checksum-sha256":
				request.Header.Set(name, value)
			default:
				return 0, fmt.Errorf("unsupported signed upload header")
			}
		}
		request.Body = &workspaceProgressBody{ReadCloser: request.Body, advance: func(n int) { transferred.Add(int64(n)) }}
		response, e := client.Do(request)
		if e != nil {
			lastErr = fmt.Errorf("pack transfer failed")
			continue
		}
		io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		response.Body.Close()
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			lastErr = nil
			break
		}
		lastErr = fmt.Errorf("pack transfer failed: %s", response.Status)
		if response.StatusCode != 429 && response.StatusCode < 500 {
			return 0, lastErr
		}
	}
	if lastErr != nil {
		return 0, lastErr
	}
	stop()
	<-done
	// Avoid waiting twice on defer: the closed channel remains readable.
	if progress != nil {
		progress("transfer_done", len(pack.Entries), len(pack.Data), time.Since(started))
		progress("pack_verify", len(pack.Entries), len(pack.Data), 0)
	}
	var result struct {
		Complete bool `json:"complete"`
	}
	if err = c.postJSON(ctx, "/v1/repositories/packs", map[string]string{"action": "complete", "id": session.ID}, token, &result); err != nil {
		return 0, err
	}
	if !result.Complete {
		return 0, fmt.Errorf("Doomer did not confirm pack verification")
	}
	return len(pack.Data), nil
}
