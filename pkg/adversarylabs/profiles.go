package adversarylabs

import (
	"fmt"
	"strings"
)

func profileAuthKey(key, profile, registryHost string) bool {
	if strings.HasPrefix(key, "auth:v2:") {
		return strings.HasSuffix(key, ":"+profile)
	}
	return profile == "default" && (key == DefaultRegistry || key == registryHost)
}

func (s ConfigStore) ProfileExists(profile, registryHost string) (bool, error) {
	config, err := s.Load()
	if err != nil {
		return false, err
	}
	for key := range config.Auths {
		if profileAuthKey(key, profile, registryHost) {
			return true, nil
		}
	}
	return false, nil
}

// RemoveProfile removes all endpoint-scoped credentials, including expired ones.
func (s ConfigStore) RemoveProfile(profile, registryHost string) error {
	return s.locked(func(config *Config) error {
		for key := range config.Auths {
			if profileAuthKey(key, profile, registryHost) {
				delete(config.Auths, key)
			}
		}
		return nil
	})
}

// RenameProfile moves credentials under one lock and never overwrites a login.
func (s ConfigStore) RenameProfile(oldName, newName, registryHost string) error {
	return s.locked(func(config *Config) error {
		for key := range config.Auths {
			if profileAuthKey(key, newName, registryHost) {
				return fmt.Errorf("profile %q already has credentials", newName)
			}
		}
		moves := make(map[string]Auth)
		for key, auth := range config.Auths {
			if !profileAuthKey(key, oldName, registryHost) {
				continue
			}
			newKey := AuthKey(DefaultAPIURL, newName)
			if strings.HasPrefix(key, "auth:v2:") {
				newKey = strings.TrimSuffix(key, ":"+oldName) + ":" + newName
			}
			if _, exists := moves[newKey]; exists {
				return fmt.Errorf("profile %q has conflicting legacy credentials; log out of the legacy login first", oldName)
			}
			moves[newKey] = auth
		}
		for key := range config.Auths {
			if profileAuthKey(key, oldName, registryHost) {
				delete(config.Auths, key)
			}
		}
		for key, auth := range moves {
			config.Auths[key] = auth
		}
		return nil
	})
}
