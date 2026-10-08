package cmd

import (
	"encoding/json"
	"fmt"
	"github.com/spf13/cobra"
	"io"
)

const outputSchemaVersion = 1

type outputEnvelope[T any] struct {
	SchemaVersion int    `json:"schemaVersion"`
	Command       string `json:"command"`
	Data          T      `json:"data"`
}

func writeJSON[T any](w io.Writer, command string, data T) error {
	return writeJSONVersion(w, outputSchemaVersion, command, data)
}

func writeJSONVersion[T any](w io.Writer, version int, command string, data T) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(outputEnvelope[T]{SchemaVersion: version, Command: command, Data: data})
}

func validateFormat(format string, legacyJSON bool) (string, error) {
	if format != "text" && format != "json" {
		return "", fmt.Errorf("--format must be text or json")
	}
	if legacyJSON && format != "text" {
		return "", fmt.Errorf("--json and --format cannot be combined")
	}
	if legacyJSON {
		return "json", nil
	}
	return format, nil
}

func commandFormat(cmd *cobra.Command, format string, legacyJSON bool) (string, error) {
	if legacyJSON && cmd.Flags().Changed("format") {
		return "", fmt.Errorf("--json and --format cannot be combined")
	}
	return validateFormat(format, legacyJSON)
}

type packDTO struct {
	Name               string           `json:"name"`
	Version            string           `json:"version"`
	Runtime            string           `json:"runtime"`
	RuntimeRequirement string           `json:"runtimeRequirement,omitempty"`
	Digest             string           `json:"digest"`
	Reference          string           `json:"reference"`
	CanonicalReference string           `json:"canonicalReference"`
	SizeBytes          int64            `json:"sizeBytes"`
	References         []string         `json:"references"`
	Files              []packFileDTO    `json:"files"`
	Warnings           []packWarningDTO `json:"warnings"`
}
type legacyPackV1DTO struct {
	Name               string   `json:"name"`
	Version            string   `json:"version"`
	Runtime            string   `json:"runtime"`
	RuntimeRequirement string   `json:"runtimeRequirement,omitempty"`
	Digest             string   `json:"digest"`
	CanonicalReference string   `json:"canonicalReference"`
	SizeBytes          int64    `json:"sizeBytes"`
	References         []string `json:"references"`
}
type packFileDTO struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"sizeBytes"`
	SHA256    string `json:"sha256"`
	Mode      string `json:"mode"`
}
type packWarningDTO struct {
	Path    string `json:"path"`
	Kind    string `json:"kind"`
	Message string `json:"message"`
}
type packCheckDTO struct {
	Name     string           `json:"name"`
	Version  string           `json:"version"`
	Runtime  string           `json:"runtime"`
	Files    []packFileDTO    `json:"files"`
	Warnings []packWarningDTO `json:"warnings"`
}
type pushDTO struct {
	CanonicalReference string `json:"canonicalReference"`
	Digest             string `json:"digest"`
}
type pullDTO struct {
	Name               string `json:"name"`
	Version            string `json:"version"`
	Tag                string `json:"tag,omitempty"`
	CanonicalReference string `json:"canonicalReference"`
	Digest             string `json:"digest"`
}

func humanSize(size int64) string {
	units := []string{"B", "KB", "MB", "GB"}
	value := float64(size)
	unit := units[0]
	for i := 1; i < len(units) && value >= 1024; i++ {
		value /= 1024
		unit = units[i]
	}
	if unit == "B" {
		return fmt.Sprintf("%d B", size)
	}
	return fmt.Sprintf("%.1f %s", value, unit)
}
