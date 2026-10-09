package application

import (
	"context"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"io"
	"time"
)

type Clock interface {
	Now() time.Time
	NewTimer(time.Duration) Timer
}
type Timer interface {
	C() <-chan time.Time
	Stop() bool
}
type AuthStore interface {
	StoredAuthE(string) (adversarylabs.Auth, bool, error)
	ExactAuthE(string) (adversarylabs.Auth, bool, error)
	SetAuth(string, adversarylabs.Auth) error
	RemoveAuthCAS(string, adversarylabs.Auth) error
}
type APIClient interface {
	BeginLogin(context.Context, adversarylabs.LoginOptions) (adversarylabs.DeviceLogin, error)
	LoginWithPassword(context.Context, adversarylabs.PasswordLoginOptions) (adversarylabs.TokenResponse, error)
	BrowserLoginURL(adversarylabs.BrowserLoginOptions) (string, error)
	ExchangeCode(context.Context, string, string, string) (adversarylabs.TokenResponse, error)
	PollToken(context.Context, string) (adversarylabs.TokenResponse, error)
	Revoke(context.Context, string) error
}
type APIFactory interface{ New(string) APIClient }
type BrowserAuthRequest struct {
	Client     APIClient
	Name, Team string
	CI         bool
	Output     io.Writer
}
type BrowserAuth interface {
	Login(context.Context, BrowserAuthRequest) (adversarylabs.TokenResponse, error)
}
type TTY interface {
	ReadSecret(context.Context, io.Reader, io.Writer) ([]byte, error)
}
type Dependencies struct {
	Clock        Clock
	Auth         AuthStore
	API          APIFactory
	BrowserAuth  BrowserAuth
	TTY          TTY
	RegistryHost string
	RegistryNS   string
	Projects     Projects
	References   References
	Resolver     Resolver
	Repository   Repository
	Registries   RegistryFactory
}
type App struct{ Deps Dependencies }

func (a *App) Dependencies() Dependencies { return a.Deps }
