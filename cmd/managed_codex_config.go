package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/doomerlabs/doomer/internal/publock"
	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

type codexPolicyStatus struct {
	ConfigPath string `json:"config_path"`
	State      string `json:"state"`
	Activation string `json:"activation"`
	Error      string `json:"error,omitempty"`
}

func codexConfigPath() (string, error) {
	home := os.Getenv("CODEX_HOME")
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		home = filepath.Join(userHome, ".codex")
	}
	return filepath.Abs(filepath.Join(home, "config.toml"))
}

func readCodexConfig(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	return data, err
}

func codexExtraPolicy(data []byte) (string, error) {
	var config struct {
		AutoReview struct {
			ExtraPolicy string `toml:"extra_policy"`
		} `toml:"auto_review"`
	}
	if err := toml.Unmarshal(data, &config); err != nil {
		return "", err
	}
	return config.AutoReview.ExtraPolicy, nil
}

func inspectCodexReviewPolicy(consent managedConsentEvidence) (*codexReviewPolicy, []byte, error) {
	p := proposedCodexReviewPolicy(consent)
	path, err := codexConfigPath()
	if err != nil {
		return nil, nil, err
	}
	p.ConfigPath = path
	data, err := readCodexConfig(path)
	if err != nil {
		return nil, nil, err
	}
	policy, err := codexExtraPolicy(data)
	if err != nil {
		return nil, nil, fmt.Errorf("read Codex reviewer policy: %w", err)
	}
	p.State = "missing"
	if strings.Contains(policy, p.ExtraPolicy) {
		p.State = "configured"
		if mergeCodexExtraPolicy(policy, consent) != policy {
			p.State = "migration_required"
		}
	} else if strings.Contains(policy, "Doomer repository review authorization\n") {
		p.State = "migration_required"
	}
	return p, data, nil
}

// Only replace complete, recognized policies for this same integration. Unknown
// edits, other accounts and other services remain byte-for-byte intact.
func mergeCodexExtraPolicy(existing string, consent managedConsentEvidence) string {
	const heading = "Doomer repository review authorization\n"
	for offset := 0; offset < len(existing); {
		i := strings.Index(existing[offset:], heading)
		if i < 0 {
			break
		}
		i += offset
		lines := strings.SplitN(existing[i:], "\n", 4)
		if len(lines) < 4 {
			break
		}
		var old struct {
			Account, Repository, Service, Profile, Agent string
			AllRepositories                              bool `json:"all_repositories"`
		}
		if err := json.Unmarshal([]byte(lines[2]), &old); err == nil && old.Account == consent.Account && old.Service == consent.Service && old.Profile == consent.Profile &&
			((consent.AllRepositories && !old.AllRepositories && old.Repository != "" && (old.Agent == "" || old.Agent == consent.Agent)) ||
				(!consent.AllRepositories && old.AllRepositories && old.Agent == "codex")) {
			legacy := proposedCodexReviewPolicy(managedConsentEvidence{Account: old.Account, Repository: old.Repository, Service: old.Service, Profile: old.Profile, Agent: old.Agent, AllRepositories: old.AllRepositories}).ExtraPolicy
			if strings.HasPrefix(existing[i:], legacy) {
				existing = existing[:i] + existing[i+len(legacy):]
				offset = i
				continue
			}
		}
		offset = i + len(heading)
	}
	p := proposedCodexReviewPolicy(consent).ExtraPolicy
	if strings.Contains(existing, p) {
		return existing
	}
	if existing != "" && !strings.HasSuffix(existing, "\n") {
		existing += "\n"
	}
	return existing + p
}

// Patch only the string token, keeping comments, formatting and other settings.
// Unusual table encodings are rejected instead of rewriting the whole config.
func patchCodexExtraPolicy(data []byte, policy string) ([]byte, error) {
	encoded, err := toml.Marshal(map[string]string{"extra_policy": policy})
	if err != nil {
		return nil, err
	}
	value := bytes.TrimSpace(bytes.SplitN(encoded, []byte("="), 2)[1])
	var parser unstable.Parser
	parser.Reset(data)
	var table []string
	insert := -1
	for parser.NextExpression() {
		n := parser.Expression()
		if n.Kind != unstable.Table && n.Kind != unstable.ArrayTable && n.Kind != unstable.KeyValue {
			continue
		}
		var key []string
		it := n.Key()
		for it.Next() {
			key = append(key, string(it.Node().Data))
		}
		if n.Kind == unstable.Table || n.Kind == unstable.ArrayTable {
			table = key
			if n.Kind == unstable.Table && len(key) == 1 && key[0] == "auto_review" {
				pos := int(n.Child().Raw.Offset)
				end := bytes.IndexByte(data[pos:], '\n')
				insert = len(data)
				if end >= 0 {
					insert = pos + end + 1
				}
			}
			continue
		}
		full := append(append([]string{}, table...), key...)
		if strings.Join(full, ".") == "auto_review.extra_policy" {
			v := n.Value()
			if v.Kind != unstable.String {
				return nil, fmt.Errorf("Codex extra_policy must be a string")
			}
			start, end := int(v.Raw.Offset), int(v.Raw.Offset+v.Raw.Length)
			return append(append(append([]byte{}, data[:start]...), value...), data[end:]...), nil
		}
	}
	if err := parser.Error(); err != nil {
		return nil, err
	}
	var out []byte
	if insert >= 0 {
		out = append(out, data[:insert]...)
		if len(out) > 0 && out[len(out)-1] != '\n' {
			out = append(out, '\n')
		}
		out = append(out, encoded...)
		out = append(out, data[insert:]...)
	} else {
		out = append(out, data...)
		out = append(out, []byte("\n[auto_review]\n")...)
		out = append(out, encoded...)
	}
	if got, err := codexExtraPolicy(out); err != nil || got != policy {
		return nil, fmt.Errorf("unsupported Codex config layout; no settings changed")
	}
	return out, nil
}

func installCodexReviewPolicy(consent managedConsentEvidence) error {
	path, err := codexConfigPath()
	if err != nil {
		return err
	}
	lock, err := publock.Acquire(filepath.Dir(path), "doomer-codex-policy")
	if err != nil {
		return err
	}
	defer lock.Close()
	data, err := readCodexConfig(path)
	if err != nil {
		return err
	}
	policy, err := codexExtraPolicy(data)
	if err != nil {
		return err
	}
	merged := mergeCodexExtraPolicy(policy, consent)
	if merged == policy {
		return nil
	}
	out, err := patchCodexExtraPolicy(data, merged)
	if err != nil {
		return err
	}
	if got, err := codexExtraPolicy(out); err != nil || got != merged {
		return fmt.Errorf("Codex policy validation failed")
	}
	mode := os.FileMode(0600)
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("Codex config must be a regular file")
		}
		mode = info.Mode().Perm()
		if err := os.WriteFile(path+".before-doomer-"+time.Now().UTC().Format("20060102T150405.000000000"), data, 0600); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".doomer-policy-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(mode); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(out); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	current, err := readCodexConfig(path)
	if err != nil {
		return err
	}
	if !bytes.Equal(data, current) {
		return fmt.Errorf("Codex config changed during migration; retry")
	}
	return os.Rename(f.Name(), path)
}
