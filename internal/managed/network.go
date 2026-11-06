package managed

import (
	"errors"
	"fmt"
	"net/url"
)

// NetworkError deliberately drops URL paths, query strings, fragments, and
// upstream bodies. Storage errors may contain signed credentials in the URL.
func NetworkError(operation string, err error) error {
	var request *url.Error
	if errors.As(err, &request) {
		parsed, parseErr := url.Parse(request.URL)
		if parseErr == nil && parsed.Hostname() != "" {
			return fmt.Errorf("%s failed contacting host %s; check network policy and retry the same task", operation, parsed.Hostname())
		}
	}
	return fmt.Errorf("%s failed; check service connectivity and retry the same task", operation)
}
