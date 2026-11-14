package cmd

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type watchEnvironment map[string]string

func (env watchEnvironment) get(key string) string {
	if value, present := env[key]; present {
		return value
	}
	return os.Getenv(key)
}

// Only the nearest file applies. Values are data, never shell expressions.
func directoryWatchEnvironment(directory string) (watchEnvironment, error) {
	directory, err := filepath.Abs(directory)
	if err != nil {
		return nil, err
	}
	directory, err = filepath.EvalSymlinks(directory)
	if err != nil {
		return nil, err
	}
	for {
		path := filepath.Join(directory, ".doomer.env")
		file, err := os.Open(path)
		if err == nil {
			defer file.Close()
			info, err := file.Stat()
			if err != nil {
				return nil, err
			}
			if !info.Mode().IsRegular() || info.Size() > 64<<10 {
				return nil, fmt.Errorf("invalid .doomer.env: expected a regular file of at most 64 KiB")
			}
			return parseWatchEnvironment(file)
		}
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("read .doomer.env: %w", err)
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return watchEnvironment{}, nil
		}
		directory = parent
	}
}

func parseWatchEnvironment(reader io.Reader) (watchEnvironment, error) {
	data, err := io.ReadAll(io.LimitReader(reader, (64<<10)+1))
	if err != nil {
		return nil, fmt.Errorf("read .doomer.env: %w", err)
	}
	if len(data) > 64<<10 {
		return nil, fmt.Errorf(".doomer.env exceeds 64 KiB")
	}
	env := watchEnvironment{}
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	scanner.Buffer(make([]byte, 4096), 64<<10)
	line := 0
	for scanner.Scan() {
		line++
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		key, value, ok := strings.Cut(text, "=")
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if !ok {
			return nil, fmt.Errorf("invalid .doomer.env assignment on line %d", line)
		}
		switch key {
		case "DOOMER_API_URL", "DOOMER_CONFIG_DIR", "DOOMER_TOKEN", "DOOMER_PROFILE":
		default:
			return nil, fmt.Errorf("unsupported .doomer.env key on line %d", line)
		}
		if _, duplicate := env[key]; duplicate {
			return nil, fmt.Errorf("duplicate .doomer.env key on line %d", line)
		}
		if strings.HasPrefix(value, "\"") || strings.HasPrefix(value, "'") {
			if len(value) < 2 || value[len(value)-1] != value[0] {
				return nil, fmt.Errorf("unclosed .doomer.env quote on line %d", line)
			}
			value = value[1 : len(value)-1]
		}
		if strings.ContainsAny(value, "\x00\r\n") {
			return nil, fmt.Errorf("invalid .doomer.env value on line %d", line)
		}
		if key == "DOOMER_API_URL" && value == "" {
			return nil, fmt.Errorf("DOOMER_API_URL cannot be empty in .doomer.env")
		}
		if key == "DOOMER_CONFIG_DIR" && value != "" && !filepath.IsAbs(value) {
			return nil, fmt.Errorf("DOOMER_CONFIG_DIR must be absolute in .doomer.env")
		}
		env[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read .doomer.env: %w", err)
	}
	return env, nil
}
