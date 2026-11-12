package livewatch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"time"
)

func Call(ctx context.Context, key string, operation string) (Response, error) {
	var out Response
	address, err := endpoint(key)
	if err != nil {
		return out, err
	}
	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "unix", address)
	if err != nil {
		return out, err
	}
	defer conn.Close()
	if !ownPeer(conn) {
		return out, fmt.Errorf("watcher belongs to a different operating-system user")
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(45 * time.Second))
	}
	if err := json.NewEncoder(conn).Encode(request{Key: key, Operation: operation}); err != nil {
		return out, err
	}
	if err := json.NewDecoder(io.LimitReader(conn, 4<<20)).Decode(&out); err != nil {
		return out, err
	}
	if out.Error != "" {
		return out, fmt.Errorf("%s", out.Error)
	}
	return out, nil
}

// Available detects an existing worker without asking it to start a review.
func Available(ctx context.Context, key string) (Response, bool) {
	probe, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	out, err := Call(probe, key, "status")
	return out, err == nil && out.Ready
}
