package security

import (
	"context"
	"errors"
)

var (
	// ErrUnauthenticated means the context carries no verified caller. HTTP
	// adapters translate it into 401.
	ErrUnauthenticated = errors.New("security: unauthenticated")
	// ErrForbidden means the caller is authenticated but may not perform the
	// operation (insufficient role or a resource it does not own). HTTP
	// adapters translate it into 403, and the business operation must not run.
	ErrForbidden = errors.New("security: forbidden")
)

// RequireAnyRole allows the caller if it has at least one of the given roles.
func RequireAnyRole(ctx context.Context, roles ...Role) error {
	c, ok := ClaimsFromContext(ctx)
	if !ok {
		return ErrUnauthenticated
	}
	if !c.HasAnyRole(roles...) {
		return ErrForbidden
	}
	return nil
}

// RequireOwnerOrRole allows the caller if it owns the resource (its subject is
// ownerSubject) or if it has one of the privileged roles (for example an
// operations analyst reviewing someone else's claim). An empty ownerSubject
// never matches.
func RequireOwnerOrRole(ctx context.Context, ownerSubject string, privileged ...Role) error {
	c, ok := ClaimsFromContext(ctx)
	if !ok {
		return ErrUnauthenticated
	}
	if ownerSubject != "" && c.Subject == ownerSubject {
		return nil
	}
	if c.HasAnyRole(privileged...) {
		return nil
	}
	return ErrForbidden
}

// RequireSamePartner allows a partner-portal user only on resources of its own
// partner: the caller must have the partner role and its "custom:partner_id"
// claim must equal partnerID. An empty partnerID never matches.
func RequireSamePartner(ctx context.Context, partnerID string) error {
	c, ok := ClaimsFromContext(ctx)
	if !ok {
		return ErrUnauthenticated
	}
	if !c.HasAnyRole(RolePartner) || partnerID == "" || c.PartnerID != partnerID {
		return ErrForbidden
	}
	return nil
}
