package main

import (
	_ "embed"
	"encoding/json"
	"html/template"
	"os"
)

//go:embed replay.html
var replayHTML string

func writeReplay(path string, r report) error {
	data, err := json.Marshal(r) // The default encoder escapes HTML and script delimiters.
	if err != nil {
		return err
	}
	page, err := template.New("replay").Parse(replayHTML)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	return page.Execute(file, template.JS(data))
}
