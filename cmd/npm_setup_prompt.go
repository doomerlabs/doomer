package cmd

import (
	"fmt"
	"os"
	"slices"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

type npmChoice struct{ value, label string }

func (p npmSetupPrompt) choose(c *cobra.Command, label string, choices []npmChoice, focus int, toggle func(int) bool) (string, error) {
	fd := int(c.InOrStdin().(*os.File).Fd())
	previous, err := term.MakeRaw(fd)
	if err != nil {
		return "", err
	}
	defer term.Restore(fd, previous)
	draw := func() {
		fmt.Fprintf(p.output, "\r\n%s\r\n", label)
		for i, item := range choices {
			marker := "  "
			if i == focus {
				marker = "› "
			}
			fmt.Fprintf(p.output, "%s%s\r\n", marker, item.label)
		}
	}
	draw()
	redraw := func() {
		fmt.Fprintf(p.output, "\x1b[%dA\r\x1b[J", len(choices)+2)
		draw()
	}
	for {
		key, err := p.input.ReadByte()
		if err != nil {
			return "", errNPMSetupCanceled
		}
		switch key {
		case 3, 4:
			return "", errNPMSetupCanceled
		case ' ', '\n', '\r':
			if toggle != nil && toggle(focus) {
				redraw()
			} else if key != ' ' {
				return choices[focus].value, nil
			}
		case 27:
			if p.input.Buffered() == 0 {
				return "", errNPMSetupCanceled
			}
			next, err := p.input.ReadByte()
			if err != nil || next != '[' {
				return "", errNPMSetupCanceled
			}
			direction, err := p.input.ReadByte()
			if err != nil {
				return "", errNPMSetupCanceled
			}
			switch direction {
			case 'A':
				focus = (focus + len(choices) - 1) % len(choices)
			case 'B':
				focus = (focus + 1) % len(choices)
			default:
				continue
			}
			redraw()
		}
	}
}

func pickNPMSetupAgents(c *cobra.Command, selected []string, p npmSetupPrompt) ([]string, error) {
	choices := []npmChoice{{"codex", ""}, {"claude", ""}, {"copilot", ""}, {"continue", "Continue"}}
	continueIndex := len(choices) - 1
	refresh := func() {
		for i, label := range []string{"Codex", "Claude Code", "GitHub Copilot CLI"} {
			status := "[ ]"
			if slices.Contains(selected, choices[i].value) {
				status = "[✓]"
			}
			choices[i].label = status + " " + label
		}
	}
	refresh()
	_, err := p.choose(c, "Which agents should Doomer set up? (↑↓ move, Space toggles, Enter confirms)", choices, continueIndex, func(i int) bool {
		if i == continueIndex {
			return false
		}
		agent := choices[i].value
		if j := slices.Index(selected, agent); j >= 0 {
			selected = slices.Delete(selected, j, j+1)
		} else {
			selected = append(selected, agent)
		}
		refresh()
		return true
	})
	return selected, err
}

func npmAskPermission(c *cobra.Command, p npmSetupPrompt, label, allow, details string) (string, error) {
	for {
		decision, err := p.choose(c, label, []npmChoice{
			{"allow", allow}, {"defer", "Not now"}, {"decline", "Don’t ask again"}, {"details", "View exact changes"},
		}, 0, nil)
		if err == errNPMSetupCanceled || decision == "defer" {
			return "", nil
		}
		if err != nil || decision != "details" {
			return decision, err
		}
		fmt.Fprintln(p.output, details)
	}
}
