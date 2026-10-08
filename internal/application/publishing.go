package application

import (
	"context"
	"github.com/doomerlabs/doomer/pkg/blobsource"
	"github.com/doomerlabs/doomer/pkg/namespacesig"
	"github.com/doomerlabs/doomer/pkg/oci"
	"github.com/doomerlabs/doomer/pkg/pack"
	"github.com/doomerlabs/doomer/pkg/repository"
)

type Projects interface {
	Check(pack.Options) (pack.Preflight, error)
	Pack(context.Context, pack.Options) (pack.Artifact, error)
}
type References interface {
	Parse(string) (oci.Reference, error)
}
type OCIRegistry interface {
	PushSources(context.Context, oci.Reference, []byte, []oci.SourceBlob) (string, error)
	PullSources(context.Context, oci.Reference) (*oci.PulledSources, error)
	// GetOfficialSignatureReferrer returns the official signature envelope for
	// imageDigest, or nil when none is present.
	GetOfficialSignatureReferrer(context.Context, oci.Reference, string) ([]byte, error)
	GetNamespaceSignatureReferrer(context.Context, oci.Reference, string) ([]byte, error)
	GetNamespaceTrustReferrer(context.Context, oci.Reference, string) ([]byte, error)
	Resolve(context.Context, oci.Reference) (string, error)
	SetPlainHTTP(bool)
}
type RegistryFactory interface {
	New(string, string) (OCIRegistry, error)
}
type Repository interface {
	BindingIdentity() string
	Resolve(string) (repository.Record, error)
	PlanGC() (repository.GCPlan, error)
	ApplyGC(repository.GCPlan, bool) (repository.GCReport, error)
	CheckAll() (repository.CheckReport, error)
	RepairAll(map[string]blobsource.Source) (repository.RepairReport, error)
	DeleteRef(string, string) error
	// ReferenceEntries lists every stored runnable reference (not collapsed by digest).
	ReferenceEntries() ([]repository.Entry, error)
	SaveOfficialSignature(digest string, envelope []byte) error
	HasVerifiedOfficialSignature(digest string) bool
	SaveNamespaceSignature(digest string, envelope, trustBundle []byte, root namespacesig.Root) error
	HasVerifiedNamespaceSignature(digest, registry, repository string) bool
	MigrationStatus(string) (repository.MigrationStatus, error)
	LeaseMaterialized(repository.Record) (*repository.MaterializationLease, error)
}
type Resolution struct {
	CanonicalReference, Digest, Path string
	Local                            bool
	Record                           repository.Record
}
type Resolver interface {
	BindingIdentity() string
	Resolve(context.Context, string) (Resolution, error)
	Lookup(context.Context, string) (Resolution, error)
	ResolveRecord(string) (repository.Record, error)
	HasExact(string) (bool, error)
	Entries(int) ([]repository.Entry, error)
	CanonicalReferenceFor(string, string) (string, error)
	Inventory(repository.Record) ([]pack.File, error)
	PayloadSources(repository.Record) (*repository.PayloadLease, error)
	ImportPacked(pack.Artifact, string) (repository.Record, error)
	ImportSources(repository.SourceImport) (repository.Record, error)
	CommitEquivalentManifest(string, string, []byte) (repository.Record, error)
	UpdateRef(string, string, string) error
}
