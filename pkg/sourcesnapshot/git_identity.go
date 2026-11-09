package sourcesnapshot

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Read effective config in the checkout so local and conditional includes take
// precedence over global values. Do not infer identity from commit authors or
// environment variables, and do not fail a review when config is unavailable.
func (g gitReader) gitIdentity() *GitIdentity {
	read := func(key string, limit int) string {
		value, err := g.text("config", "--get", key)
		if err != nil || len(value) > limit || !utf8.ValidString(value) || strings.ContainsFunc(value, unicode.IsControl) {
			return ""
		}
		return value
	}
	name, email := read("user.name", 256), read("user.email", 320)
	if name == "" && email == "" {
		return nil
	}
	return &GitIdentity{Name: name, Email: email, Source: "git_config"}
}
