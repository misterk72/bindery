package auth

// The three user roles. The users.role column stores these literals.
//
//   - RoleAdmin manages the instance: settings, users, indexers, clients.
//   - RoleUser runs a library: adds, searches, grabs and imports.
//   - RoleRequester can browse a read only projection of the library and ask
//     for a book or an author. An admin approves or declines each request.
//     Every API route a requester may call is listed in requester.go; anything
//     else answers 403.
const (
	RoleAdmin     = "admin"
	RoleUser      = "user"
	RoleRequester = "requester"
)

// ValidRole reports whether role is one of the three role literals. Every
// place that accepts a role from outside (the user management API, OIDC
// provisioning, the BINDERY_OIDC_DEFAULT_ROLE variable, the repository
// setters) checks with this one function, so a fourth role is one edit here.
// The match is exact: callers that accept free form input normalise case and
// whitespace first.
func ValidRole(role string) bool {
	switch role {
	case RoleAdmin, RoleUser, RoleRequester:
		return true
	default:
		return false
	}
}
