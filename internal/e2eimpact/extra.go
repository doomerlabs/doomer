package e2eimpact

import (
	"fmt"
	"io"
	"path"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

type Extras struct {
	Version           int                 `yaml:"version"`
	ExtraDependencies map[string][]string `yaml:"extra_dependencies"`
}

func ReadExtras(reader io.Reader) (Extras, error) {
	var out Extras
	decoder := yaml.NewDecoder(reader)
	decoder.KnownFields(true)
	if err := decoder.Decode(&out); err != nil {
		return out, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return out, fmt.Errorf("expected exactly one dependency document")
	}
	if out.Version != 1 {
		return out, fmt.Errorf("unsupported dependency manifest version %d", out.Version)
	}
	return out, nil
}

// Expand only adds dependencies. Unknown tests and unmatched file patterns are
// errors so a typo cannot silently shrink a test's inputs.
func (e Extras) Expand(files []string, tests []string) (map[string][]string, error) {
	if e.Version != 1 {
		return nil, fmt.Errorf("unsupported dependency manifest version %d", e.Version)
	}
	out := map[string][]string{}
	for _, id := range tests {
		if id == "" {
			return nil, fmt.Errorf("empty test ID")
		}
		if _, exists := out[id]; exists {
			return nil, fmt.Errorf("duplicate test ID: %s", id)
		}
		out[id] = nil
	}
	patterns := make([]string, 0, len(e.ExtraDependencies))
	for pattern := range e.ExtraDependencies {
		patterns = append(patterns, pattern)
	}
	sort.Strings(patterns)
	for _, pattern := range patterns {
		if !cleanPath(pattern) {
			return nil, fmt.Errorf("invalid dependency pattern %q", pattern)
		}
		var matchedFiles []string
		for _, file := range files {
			if !cleanPath(file) {
				return nil, fmt.Errorf("invalid source path %q", file)
			}
			match, err := matchGlob(pattern, file)
			if err != nil {
				return nil, err
			}
			if match {
				matchedFiles = append(matchedFiles, file)
			}
		}
		if len(matchedFiles) == 0 {
			return nil, fmt.Errorf("dependency pattern matches no files: %s", pattern)
		}
		selectors := e.ExtraDependencies[pattern]
		if len(selectors) == 0 {
			return nil, fmt.Errorf("dependency pattern has no test selectors: %s", pattern)
		}
		for _, selector := range selectors {
			matches := 0
			for _, id := range tests {
				match, err := path.Match(selector, id)
				if selector == "*" {
					match = true
				}
				if err != nil {
					return nil, fmt.Errorf("invalid test selector %q: %w", selector, err)
				}
				if match {
					out[id] = append(out[id], matchedFiles...)
					matches++
				}
			}
			if matches == 0 {
				return nil, fmt.Errorf("test selector matches no tests: %s", selector)
			}
		}
	}
	for id := range out {
		sort.Strings(out[id])
		out[id] = unique(out[id])
	}
	return out, nil
}

func matchGlob(pattern, file string) (bool, error) {
	p, f := strings.Split(pattern, "/"), strings.Split(file, "/")
	for _, part := range p {
		if part != "**" {
			if _, err := path.Match(part, ""); err != nil {
				return false, fmt.Errorf("invalid dependency pattern %q: %w", pattern, err)
			}
		}
	}
	type state struct{ pattern, file int }
	memo := map[state]bool{}
	visited := map[state]bool{}
	var match func(int, int) bool
	match = func(i, j int) bool {
		key := state{i, j}
		if visited[key] {
			return memo[key]
		}
		visited[key] = true
		result := false
		defer func() { memo[key] = result }()
		if i == len(p) {
			result = j == len(f)
			return result
		}
		if p[i] == "**" {
			result = match(i+1, j) || (j < len(f) && match(i, j+1))
			return result
		}
		if j == len(f) {
			return false
		}
		ok, _ := path.Match(p[i], f[j])
		result = ok && match(i+1, j+1)
		return result
	}
	return match(0, 0), nil
}
