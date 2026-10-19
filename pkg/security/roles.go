package security

// Role is a human role of the platform. Its value is the name of the Cognito
// group the user belongs to.
type Role string

// The four human roles. Each user belongs to exactly one group, assigned by an
// administrator when the user is provisioned; nobody can assign it to itself.
const (
	RoleInsured    Role = "insured"
	RoleAdvisor    Role = "advisor"
	RoleOperations Role = "operations"
	RolePartner    Role = "partner"
)

// ParseRole maps a Cognito group name to a Role. It reports false for groups
// that are not one of the four human roles.
func ParseRole(group string) (Role, bool) {
	switch r := Role(group); r {
	case RoleInsured, RoleAdvisor, RoleOperations, RolePartner:
		return r, true
	default:
		return "", false
	}
}
