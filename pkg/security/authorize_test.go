package security

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func ctxWith(c Claims) context.Context {
	return WithClaims(context.Background(), c)
}

func TestRequireAnyRole(t *testing.T) {
	advisor := ctxWith(Claims{Subject: "s-1", Roles: []Role{RoleAdvisor}})

	assert.NoError(t, RequireAnyRole(advisor, RoleAdvisor))
	assert.NoError(t, RequireAnyRole(advisor, RoleOperations, RoleAdvisor))
	assert.ErrorIs(t, RequireAnyRole(advisor, RoleOperations), ErrForbidden)
	assert.ErrorIs(t, RequireAnyRole(advisor), ErrForbidden)
	assert.ErrorIs(t, RequireAnyRole(ctxWith(Claims{Subject: "s-2"}), RoleInsured), ErrForbidden)
	assert.ErrorIs(t, RequireAnyRole(context.Background(), RoleAdvisor), ErrUnauthenticated)
}

func TestRequireOwnerOrRole_OwnerIsAllowed(t *testing.T) {
	insured := ctxWith(Claims{Subject: "owner-1", Roles: []Role{RoleInsured}})
	assert.NoError(t, RequireOwnerOrRole(insured, "owner-1", RoleOperations))
}

func TestRequireOwnerOrRole_ForeignResourceIsForbidden(t *testing.T) {
	insured := ctxWith(Claims{Subject: "owner-1", Roles: []Role{RoleInsured}})
	assert.ErrorIs(t, RequireOwnerOrRole(insured, "owner-2", RoleOperations), ErrForbidden)
}

func TestRequireOwnerOrRole_PrivilegedRoleIsAllowed(t *testing.T) {
	analyst := ctxWith(Claims{Subject: "analyst-1", Roles: []Role{RoleOperations}})
	assert.NoError(t, RequireOwnerOrRole(analyst, "owner-2", RoleOperations))
}

func TestRequireOwnerOrRole_EmptyOwnerNeverMatches(t *testing.T) {
	noSubject := ctxWith(Claims{Roles: []Role{RoleInsured}})
	assert.ErrorIs(t, RequireOwnerOrRole(noSubject, ""), ErrForbidden)
}

func TestRequireOwnerOrRole_WithoutClaimsIsUnauthenticated(t *testing.T) {
	assert.ErrorIs(t, RequireOwnerOrRole(context.Background(), "owner-1"), ErrUnauthenticated)
}

func TestRequireSamePartner_SamePartnerIsAllowed(t *testing.T) {
	partner := ctxWith(Claims{Subject: "p-user", PartnerID: "partner-7", Roles: []Role{RolePartner}})
	assert.NoError(t, RequireSamePartner(partner, "partner-7"))
}

func TestRequireSamePartner_OtherPartnerIsForbidden(t *testing.T) {
	partner := ctxWith(Claims{Subject: "p-user", PartnerID: "partner-7", Roles: []Role{RolePartner}})
	assert.ErrorIs(t, RequireSamePartner(partner, "partner-8"), ErrForbidden)
	assert.ErrorIs(t, RequireSamePartner(partner, ""), ErrForbidden)
}

func TestRequireSamePartner_RequiresPartnerRole(t *testing.T) {
	advisor := ctxWith(Claims{Subject: "a-user", PartnerID: "partner-7", Roles: []Role{RoleAdvisor}})
	assert.ErrorIs(t, RequireSamePartner(advisor, "partner-7"), ErrForbidden)

	noPartnerID := ctxWith(Claims{Subject: "p-user", Roles: []Role{RolePartner}})
	assert.ErrorIs(t, RequireSamePartner(noPartnerID, ""), ErrForbidden)
}

func TestRequireSamePartner_WithoutClaimsIsUnauthenticated(t *testing.T) {
	assert.ErrorIs(t, RequireSamePartner(context.Background(), "partner-7"), ErrUnauthenticated)
}
