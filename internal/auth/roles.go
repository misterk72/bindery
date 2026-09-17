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

// RoleHasLibraryAccess reports whether role may use the library directly:
// the full API, OPDS feeds and file downloads. True for admin and user only.
// A requester, an empty role (a user whose role could not be read) and any
// unknown value are refused, so a lookup failure fails closed.
func RoleHasLibraryAccess(role string) bool {
	return role == RoleAdmin || role == RoleUser
}
