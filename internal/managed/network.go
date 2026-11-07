package managed

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/doomerlabs/doomer/pkg/adversarylabs"
)

// ConnectionError retains only a hostname and safe diagnostics for setup help.
// The original URL error may contain signed URLs and must not be retained.
type ConnectionError struct {
	Host string
	err  error
}

func (e *ConnectionError) Error() string { return e.err.Error() }
func (e *ConnectionError) Unwrap() error { return e.err }

// NetworkError deliberately drops URL paths, query strings, fragments, and
// upstream bodies. Storage errors may contain signed credentials in the URL.
func NetworkError(operation string, err error) error {
	var response *adversarylabs.ResponseError
	if errors.As(err, &response) {
		safe := fmt.Errorf("%s failed: %w", operation, response)
		// A recognized service error has its own diagnosis. An unrecognized 403
		// may come from a network proxy, but is not proof of a policy denial.
		if response.StatusCode == http.StatusForbidden && response.Code == "" && response.Host != "" {
			return &ConnectionError{Host: response.Host, err: safe}
		}
		return safe
	}
	var request *url.Error
	if errors.As(err, &request) {
		parsed, parseErr := url.Parse(request.URL)
		if parseErr == nil && parsed.Hostname() != "" {
			host := parsed.Hostname()
			return &ConnectionError{Host: host, err: fmt.Errorf("%s failed contacting host %s; check network policy and retry the same task", operation, host)}
		}
	}
	return fmt.Errorf("%s failed; check service connectivity and retry the same task", operation)
}
