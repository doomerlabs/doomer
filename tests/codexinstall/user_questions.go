package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

func codexUserQuestions(env []string, session string) []string {
	if session == "" {
		return nil
	}
	home := ""
	for _, entry := range env {
		if strings.HasPrefix(entry, "CODEX_HOME=") {
			home = strings.TrimPrefix(entry, "CODEX_HOME=")
		}
	}
	if home == "" {
		return nil
	}
	files, _ := filepath.Glob(filepath.Join(home, "sessions", "*", "*", "*", "*"+session+".jsonl"))
	var questions []string
	for _, path := range files {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 65536), 4000000)
		for scanner.Scan() {
			var entry struct {
				Type    string `json:"type"`
				Payload struct {
					Type      string `json:"type"`
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"payload"`
			}
			if json.Unmarshal(scanner.Bytes(), &entry) != nil || entry.Type != "response_item" || entry.Payload.Type != "function_call" || entry.Payload.Name != "request_user_input_async" {
				continue
			}
			var args struct {
				Questions []struct {
					Title string `json:"title"`
				} `json:"questions"`
			}
			if json.Unmarshal([]byte(entry.Payload.Arguments), &args) != nil {
				continue
			}
			for _, q := range args.Questions {
				if q.Title != "" {
					questions = append(questions, q.Title)
				}
			}
		}
		f.Close()
	}
	return questions
}
