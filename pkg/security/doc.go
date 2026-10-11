// Package security authenticates and authorizes the human channels of Solventa
// (insured, commercial advisor, operations analyst and partner-portal user)
// with the JSON Web Tokens issued by the Amazon Cognito user pool (HA-S01).
//
// # What it does
//
//   - Models the four human roles as the Cognito groups "insured", "advisor",
//     "operations" and "partner" (one group per user).
//   - Turns the token claims into a [Claims] value (subject, username, email,
//     roles, the partner ownership claim "custom:partner_id", token use,
//     client and validity) and carries it in the request context
//     ([WithClaims], [ClaimsFromContext]).
//   - Offers authorization helpers for use cases ([RequireAnyRole],
//     [RequireOwnerOrRole], [RequireSamePartner]). They only depend on the
//     standard library, so internal/application may call them without
//     breaking the ports-and-adapters boundary.
//
// # What it does not do
//
// It does not check token revocation: a revoked token stays valid for any
// signature-and-expiry check until it expires, so revocation relies on short
// token lifetimes (see docs/api/README.md, "Autenticación y autorización").
//
// # Where it is mounted
//
// Only on groups of human routes. GET /health and machine-to-machine partner
// routes (API key and usage plan at the API Gateway) never go through it.
package security
