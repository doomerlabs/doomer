package cmd

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/doomerlabs/doomer/internal/application"
	"github.com/doomerlabs/doomer/internal/paths"
	"github.com/doomerlabs/doomer/pkg/oci"
	"github.com/doomerlabs/doomer/pkg/pack"
	"github.com/doomerlabs/doomer/pkg/repository"
)

type publishingReferences struct{ registry, namespace string }

func (p publishingReferences) Parse(value string) (oci.Reference, error) {
	return oci.ParseReferenceWithDefaults(value, p.registry, p.namespace)
}

type publishingProjects struct {
	references application.References
	stateDir   string
	build      pack.BuildEnvironment
}

func (p publishingProjects) Check(opts pack.Options) (pack.Preflight, error) {
	opts.ParseReference = p.references.Parse
	return pack.Check(opts)
}
func (p publishingProjects) Pack(ctx context.Context, opts pack.Options) (pack.Artifact, error) {
	opts.ParseReference = p.references.Parse
	opts.BuildProject = func(ctx context.Context, opts pack.BuildOptions) error {
		opts.BuildStateDir = p.stateDir
		return pack.BuildProjectWithEnvironment(ctx, opts, p.build)
	}
	return pack.Create(ctx, opts)
}

type publishingResolver struct{ repository.Repository }

func (p publishingResolver) BindingIdentity() string { return p.RootPath() }
func (p publishingResolver) ResolveRecord(value string) (repository.Record, error) {
	return p.Repository.Resolve(value)
}
func (p publishingResolver) Lookup(ctx context.Context, value string) (application.Resolution, error) {
	if err := ctx.Err(); err != nil {
		return application.Resolution{}, err
	}
	record, err := p.Repository.Resolve(value)
	return application.Resolution{Record: record, Digest: record.Digest}, err
}
func (p publishingResolver) Resolve(ctx context.Context, value string) (application.Resolution, error) {
	return p.Lookup(ctx, value)
}

type publishingRepository struct{ repository.Repository }

func (p publishingRepository) BindingIdentity() string { return p.RootPath() }

type publishingRegistry struct{ *oci.HTTPRegistry }

func (r publishingRegistry) SetPlainHTTP(v bool) { r.PlainHTTP = v }

type publishingRegistryFactory struct {
	store  application.AuthStore
	host   string
	docker oci.CredentialStore
	tokens *oci.BearerTokenCache
}

func (f publishingRegistryFactory) New(apiURL, profile string) (application.OCIRegistry, error) {
	r := oci.NewHTTPRegistry()
	r.TokenCache = f.tokens
	r.BearerRealm = registryAuthRealm(apiURL)
	r.BearerService = f.host
	if realm, err := url.Parse(r.BearerRealm); err == nil && realm.Host != "" {
		r.TokenAuthorities[f.host] = oci.TokenAuthority{Origin: realm.Scheme + "://" + realm.Host, Service: f.host}
	}
	auth, ok, err := scopedAuth(f.store, apiURL, profile, f.host)
	if err != nil {
		return nil, err
	}
	stores := oci.ChainCredentialStore{f.docker}
	if ok {
		stores = append(oci.ChainCredentialStore{scopedCredentialStore{registry: f.host, token: auth.Token}}, stores...)
	}
	r.Credentials = stores
	return publishingRegistry{r}, nil
}

// Compose publishing dependencies only for artifact commands. Authentication
// and version commands should not need artifact storage or build tools.
func configurePublishing(app *application.App, createStore bool) error {
	root, err := paths.DataDir()
	if err != nil {
		return err
	}
	state, err := pack.ResolveBuildStateDir("")
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	namespace := os.Getenv("DOOMER_REGISTRY_NAMESPACE")
	host := app.Deps.RegistryHost
	refs := publishingReferences{host, namespace}
	repo := repository.Repository{Root: filepath.Join(root, "repository-v1"), DefaultRegistry: host, DefaultNamespace: namespace}
	if createStore {
		if err := os.MkdirAll(repo.Root, 0700); err != nil {
			return err
		}
	}
	if entries, readErr := os.ReadDir(filepath.Join(repo.Root, "transactions")); readErr == nil && len(entries) > 0 {
		if err := repo.Recover(); err != nil {
			return err
		}
	} else if readErr != nil && !os.IsNotExist(readErr) {
		return readErr
	}
	npm, npmErr := exec.LookPath("npm")
	node, nodeErr := exec.LookPath("node")
	docker, dockerErr := exec.LookPath("docker")
	environment := os.Environ()
	build := pack.BuildEnvironment{NPM: npm, NPMError: npmErr, Node: node, NodeError: nodeErr, Docker: docker, DockerError: dockerErr, Environment: environment, Run: func(ctx context.Context, executable string, args []string, dir string, env []string, stdout, stderr io.Writer, capture bool) ([]byte, error) {
		command := exec.CommandContext(ctx, executable, args...)
		command.Dir = dir
		command.Env = env
		command.Stdout = stdout
		command.Stderr = stderr
		command.WaitDelay = 2 * time.Second
		var output boundedBuildOutput
		if capture {
			command.Stdout = &output
		}
		err := command.Run()
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return output.Bytes(), err
	}}
	credentials := oci.DockerCredentialStore{HomeDir: home, Lstat: os.Lstat, Open: oci.OpenRegularNoFollow, RunHelper: func(ctx context.Context, helper, server string) ([]byte, error) {
		command := exec.CommandContext(ctx, helper, "get")
		command.Stdin = bytes.NewBufferString(server + "\n")
		command.Env = environment
		command.WaitDelay = 2 * time.Second
		var output boundedBuildOutput
		command.Stdout = &output
		err := command.Run()
		return output.Bytes(), err
	}}
	app.Deps.RegistryNS = namespace
	app.Deps.References = refs
	app.Deps.Repository = publishingRepository{repo}
	app.Deps.Resolver = publishingResolver{repo}
	app.Deps.Projects = publishingProjects{refs, state, build}
	app.Deps.Registries = publishingRegistryFactory{app.Deps.Auth, host, credentials, oci.NewBearerTokenCache()}
	return nil
}

type boundedBuildOutput struct{ bytes.Buffer }

func (b *boundedBuildOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 1<<20 {
		return 0, fmt.Errorf("build probe output exceeds 1 MiB")
	}
	return b.Buffer.Write(p)
}
