package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doomerlabs/doomer/pkg/pack"
	"github.com/doomerlabs/doomer/pkg/repository"
)

func TestAdversaryCommandTree(t *testing.T) {
	root := NewRootCommand()
	group, _, err := root.Find([]string{"adversary"})
	if err != nil || group.Name() != "adversary" {
		t.Fatalf("adversary group: %v", err)
	}
	for _, name := range []string{"init", "ls", "pack", "push", "pull", "inspect", "rm", "check"} {
		command, _, err := root.Find([]string{"adversary", name})
		if err != nil || command.Parent() != group {
			t.Errorf("missing adversary %s: %v", name, err)
		}
	}
	for _, name := range []string{"init", "pack", "push", "pull", "artifacts"} {
		if _, _, err := root.Find([]string{name}); err == nil {
			t.Errorf("obsolete root command %s remains", name)
		}
	}
}

func TestAdversaryInitAndProfileListingDoNotOpenArtifactStorage(t *testing.T) {
	isolate(t)
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOOMER_DATA_DIR", blocked)
	parent := t.TempDir()
	out, err := execute(t, "", "adversary", "init", "test-reviewer", "--path", parent, "--sdk", "typescript")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"doomer adversary pack . --check", "doomer adversary push test-reviewer:"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing next step %q in %s", want, out)
		}
	}
	if _, err := os.Stat(filepath.Join(parent, "test-reviewer", "adversary.yaml")); err != nil {
		t.Fatal(err)
	}
	if _, err := execute(t, "", "profile", "ls"); err != nil {
		t.Fatal(err)
	}
}

func TestAdversaryLocalReferences(t *testing.T) {
	for _, scenario := range []string{"short-tag", "canonical", "ambiguous-name", "ambiguous-digest", "wrong-digest", "correct-digest", "invalid-digest", "missing"} {
		t.Run(scenario, func(t *testing.T) {
			isolate(t)
			data := t.TempDir()
			t.Setenv("DOOMER_DATA_DIR", data)
			t.Setenv("DOOMER_REGISTRY_HOST", "registry.example")
			t.Setenv("DOOMER_REGISTRY_NAMESPACE", "team")
			project := t.TempDir()
			writeProject(t, project)
			artifact, err := pack.Create(context.Background(), pack.Options{Dir: project})
			if err != nil {
				t.Fatal(err)
			}
			defer artifact.Close()
			repo := repository.Repository{Root: filepath.Join(data, "repository-v1"), DefaultRegistry: "registry.example", DefaultNamespace: "team"}
			if err := os.MkdirAll(repo.Root, 0700); err != nil {
				t.Fatal(err)
			}
			canonical := "registry.example/team/reviewer:1.4.2"
			record, err := repo.ImportPacked(artifact, canonical)
			if err != nil {
				t.Fatal(err)
			}
			other := "registry.example/team/reviewer:latest"
			if err := repo.UpdateRef(other, "", record.Digest); err != nil {
				t.Fatal(err)
			}
			var listed outputEnvelope[[]repository.Entry]
			if err := json.Unmarshal(publishCommand(t, "adversary", "ls", "--format", "json"), &listed); err != nil {
				t.Fatal(err)
			}
			if len(listed.Data) != 2 {
				t.Fatalf("ls must include each removable reference: %+v", listed.Data)
			}
			args := []string{"adversary", "rm", "reviewer:1.4.2"}
			wantError := ""
			switch scenario {
			case "canonical":
				args[2] = canonical
			case "ambiguous-name":
				args[2] = "reviewer"
				wantError = "ambiguous"
			case "ambiguous-digest":
				args[2] = record.Digest
				wantError = "ambiguous"
			case "wrong-digest":
				args = append(args, "--if-digest", "sha256:"+strings.Repeat("0", 64))
				wantError = "CAS"
			case "correct-digest":
				args = append(args, "--if-digest", record.Digest)
			case "invalid-digest":
				args = append(args, "--if-digest", "")
				wantError = "invalid --if-digest"
			case "missing":
				args[2] = "unknown:1"
				wantError = "not found"
			}
			out, err := execute(t, "", args...)
			if wantError != "" {
				if wantError == "CAS" {
					if !errors.Is(err, repository.ErrCAS) {
						t.Fatalf("expected digest conflict: %v", err)
					}
				} else if err == nil || !strings.Contains(err.Error(), wantError) {
					t.Fatalf("expected %s: %v", wantError, err)
				}
				if _, err := repo.Resolve(canonical); err != nil {
					t.Fatalf("failed removal changed reference: %v", err)
				}
			} else {
				if err != nil || !strings.Contains(out, "Removed "+canonical) {
					t.Fatalf("rm: %s %v", out, err)
				}
				if _, err := repo.Resolve(canonical); !os.IsNotExist(err) {
					t.Fatalf("reference remains: %v", err)
				}
			}
			if _, err := repo.Resolve(other); err != nil {
				t.Fatalf("other reference damaged: %v", err)
			}
			publishCommand(t, "adversary", "check")
		})
	}
}
