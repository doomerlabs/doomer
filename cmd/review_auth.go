package cmd

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/doomerlabs/doomer/internal/application"
	watching "github.com/doomerlabs/doomer/internal/watch"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
)

// Anonymous capabilities are separate from login credentials and scoped to the
// exact service/profile. A real login always takes precedence.
func reviewAuth(app *application.App, api, profile string) (adversarylabs.Auth, error) {
	auth, ok, err := scopedAuth(app.Dependencies().Auth, api, profile, app.Dependencies().RegistryHost)
	if err != nil {
		return auth, err
	}
	if ok && strings.TrimSpace(auth.Token) != "" {
		return auth, nil
	}
	root, _, err := watchPaths(api, profile)
	if err != nil {
		return auth, err
	}
	if err = os.MkdirAll(root, 0700); err != nil {
		return auth, err
	}
	file := filepath.Join(root, "anonymous.json")
	var saved struct{ API, Profile, Token string }
	load := func() error {
		if e := watching.Read(file, &saved); e != nil {
			return e
		}
		if saved.API != api || saved.Profile != profile || !adversarylabs.IsAnonymousToken(saved.Token) {
			return fmt.Errorf("invalid anonymous review state")
		}
		auth.Token = saved.Token
		return nil
	}
	if e := load(); e == nil {
		return auth, nil
	} else if !os.IsNotExist(e) {
		return auth, e
	}
	err = watching.Locked(root, "anonymous-auth", func() error {
		if e := load(); e == nil {
			return nil
		} else if !os.IsNotExist(e) {
			return e
		}

		var secret [32]byte
		if _, e := rand.Read(secret[:]); e != nil {
			return e
		}
		saved.API, saved.Profile, saved.Token = api, profile, adversarylabs.AnonymousTokenPrefix+base64.RawURLEncoding.EncodeToString(secret[:])
		if e := watching.Write(file, saved); e != nil {
			return e
		}
		auth.Token = saved.Token
		return nil
	})
	return auth, err
}
