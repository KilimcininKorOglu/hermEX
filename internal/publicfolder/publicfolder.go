// Package publicfolder routes callers to their own domain's public-folder store
// and lists the folders each caller may see. It is the single home of the
// tenant-isolation invariant for public folders: the domain a caller reaches is
// always derived from the authenticated caller's own address, never supplied by
// the caller, so no protocol surface (EWS, IMAP, webmail, admin) can be tricked
// into opening another domain's public tree. Every surface goes through this
// service rather than opening a public store directly.
//
// A per-domain public store mirrors the reference store model: each domain owns a
// distinct store under its home directory, and a domain "has" public folders
// exactly when that store has been provisioned. There is no separate runtime
// enable flag — provision implies enabled, un-provisioned implies absent.
package publicfolder

import (
	"errors"
	"strings"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// Paths supplies the per-domain public-store directory. *config.Config satisfies
// it via HomedirFor, the documented domain public-store directory.
type Paths interface {
	HomedirFor(domain string) string
}

// Service resolves callers to their domain's public-folder store. It holds no
// store handles; each call opens and closes a store, matching the rest of the
// codebase's per-operation store lifetime.
type Service struct {
	paths Paths
}

// New returns a Service resolving public stores through paths.
func New(paths Paths) *Service { return &Service{paths: paths} }

// Folder is a public folder a caller may see, carrying the caller's effective
// rights so a surface can decide read versus post without a second resolve.
type Folder struct {
	ID          int64
	DisplayName string
	Rights      uint32
}

// domainOf extracts the lowercased domain from an email address, or "" when the
// address carries no domain.
func domainOf(addr string) string {
	_, domain, _ := strings.Cut(strings.ToLower(addr), "@")
	return domain
}

// Provision ensures the domain's public store exists, creating and seeding it if
// absent. It is idempotent and is the administrator's enable action: after it the
// domain has public folders. domain is taken as given (an administrative input),
// not derived from a caller.
func (svc *Service) Provision(domain string) error {
	st, err := objectstore.OpenPublic(svc.paths.HomedirFor(strings.ToLower(domain)))
	if err != nil {
		return err
	}
	return st.Close()
}

// OpenForDomain opens an administrative handle on a domain's public store (read
// and write), without creating it. It returns ErrNotProvisioned when the domain
// has no public store. The caller owns the returned store and must Close it. This
// is for admin management, which addresses a domain directly; caller-facing
// surfaces use OpenForCaller so tenant isolation cannot be bypassed.
func (svc *Service) OpenForDomain(domain string) (*objectstore.Store, error) {
	return objectstore.OpenPublicExisting(svc.paths.HomedirFor(strings.ToLower(domain)))
}

// OpenForCaller opens the public store of the CALLER's own domain for reading,
// without creating it. ok is false (with a nil store and nil error) when the
// caller's domain has no public store, so a surface renders an empty result
// rather than failing. The domain is taken from callerEmail alone — the
// tenant-isolation chokepoint. The caller owns the returned store and must Close
// it when ok is true.
func (svc *Service) OpenForCaller(callerEmail string) (st *objectstore.Store, ok bool, err error) {
	domain := domainOf(callerEmail)
	if domain == "" {
		return nil, false, nil
	}
	st, err = objectstore.OpenPublicExisting(svc.paths.HomedirFor(domain))
	if errors.Is(err, objectstore.ErrNotProvisioned) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return st, true, nil
}

// VisibleFolders returns the public folders in the caller's own domain that the
// caller may see — those on which the caller's effective rights include
// FrightsVisible — each with those rights. It returns nil when the caller's
// domain has no public store. This is the shared discovery the EWS, IMAP, and
// webmail surfaces all render.
func (svc *Service) VisibleFolders(callerEmail string) ([]Folder, error) {
	st, ok, err := svc.OpenForCaller(callerEmail)
	if err != nil || !ok {
		return nil, err
	}
	defer st.Close()

	all, err := st.ListFolders()
	if err != nil {
		return nil, err
	}
	user := strings.ToLower(callerEmail)
	var out []Folder
	for _, f := range all {
		rights, err := st.ResolvePermission(f.ID, user)
		if err != nil {
			return nil, err
		}
		if rights&mapi.FrightsVisible == 0 {
			continue
		}
		out = append(out, Folder{ID: f.ID, DisplayName: f.DisplayName, Rights: rights})
	}
	return out, nil
}
