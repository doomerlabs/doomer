package main

import "testing"

func TestLoadedDoomerSkillDoesNotDependOnInstructionWording(t *testing.T) {
	for _, instructions := range []string{"At task start, run preflight.", "For coding, setup, or review tasks, start with preflight."} {
		text := "---\nname: doomer-review\n---\n# Doomer review\n" + instructions
		if !loadedDoomerSkill("Command", text) {
			t.Fatalf("skill content not recognized: %s", instructions)
		}
		if loadedDoomerSkill("Codex", text) {
			t.Fatal("agent narration counted as loading the skill")
		}
	}
	if loadedDoomerSkill("Command", "cat .agents/skills/doomer-review/SKILL.md") {
		t.Fatal("command without content counted as loading")
	}
	if !loadedDoomerSkill("Command", `Skill {"skill":"doomer-review"}`) {
		t.Fatal("native skill invocation not recognized")
	}
}
