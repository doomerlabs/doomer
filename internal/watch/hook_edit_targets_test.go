package watch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestHookEditRepositories(t *testing.T) {
	chat, sibling := repository(t), repository(t)
	alias := filepath.Join(t.TempDir(), "checkout-alias")
	aliasErr := os.Symlink(sibling, alias)
	siblingFile := filepath.Join(sibling, "new", "file.txt")
	patch := "*** Begin Patch\n*** Add File: " + siblingFile + "\n+hello\n*** Update File: " + filepath.Join(chat, "file.txt") + "\n*** End Patch"
	type editCase struct {
		name  string
		tool  string
		input any
		want  []string
	}
	cases := []editCase{
		{"shell writes in both repos", "Bash", map[string]string{
			"command": "printf hello > file.txt; printf hello > " + quotedJSON(siblingFile),
		}, []string{chat, sibling}},
		{"direct patch across repos", "apply_patch", map[string]string{"command": patch}, []string{sibling, chat}},
		{"explicit shell workdir", "Bash", map[string]string{
			"workdir": sibling,
			"command": "printf hello > new/file.txt",
		}, []string{sibling}},
		{"shell changes to a literal directory before writing", "Bash", map[string]string{
			"command": "cd " + quotedJSON(sibling) + " && printf hello > new/file.txt && pwd",
		}, []string{sibling}},
		{"chained literal directory changes", "Bash", map[string]string{
			"command": "cd " + quotedJSON(chat) + " && cd " + quotedJSON(sibling) + " && printf hello > new/file.txt",
		}, []string{sibling}},
		{"read only after changing directory", "Bash", map[string]string{
			"command": "cd " + quotedJSON(sibling) + " && cat file.txt",
		}, nil},
		{"in-place command with explicit workdir", "Bash", map[string]string{
			"workdir": sibling,
			"command": "sed -i s/old/new/ file.txt",
		}, []string{sibling}},
		{"read only", "Bash", map[string]string{
			"workdir": sibling,
			"command": "cat file.txt",
		}, nil},
	}
	if aliasErr == nil {
		cases = append(cases, editCase{"canonical checkout behind symlink", "Bash", map[string]string{
			"command": "printf hello > " + quotedJSON(filepath.Join(alias, "new/file.txt")),
		}, []string{sibling}})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.input)
			if err != nil {
				t.Fatal(err)
			}
			got := HookEditRepositories(t.Context(), HookInput{CWD: chat, Tool: tc.tool, Input: raw})
			if !slices.Equal(got, tc.want) {
				t.Fatalf("resolved repositories %v, want %v", got, tc.want)
			}
		})
	}
}
