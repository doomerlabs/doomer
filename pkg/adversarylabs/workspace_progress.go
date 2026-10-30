package adversarylabs

import (
	"context"
	"io"
)

type workspaceTransferProgressKey struct{}
type workspaceProgressBody struct {
	io.ReadCloser
	advance func(int)
}

func (b *workspaceProgressBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.advance(n)
	}
	return n, err
}
func trackWorkspaceBody(ctx context.Context, body io.ReadCloser) io.ReadCloser {
	if advance, ok := ctx.Value(workspaceTransferProgressKey{}).(func(int)); ok && body != nil {
		return &workspaceProgressBody{ReadCloser: body, advance: advance}
	}
	return body
}
