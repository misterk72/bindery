package pathmap

import "strings"

// JoinClientPath joins rel onto base in the download client's own namespace,
// not Bindery's: a Windows base written with backslashes keeps backslashes,
// anything else uses forward slashes. Used to build folders a client derives
// from its settings, such as a default save path plus a category name.
func JoinClientPath(base, rel string) string {
	base = strings.TrimSpace(base)
	rel = strings.Trim(strings.TrimSpace(rel), `/\`)
	if rel == "" {
		return base
	}
	sep := "/"
	if IsWindowsPath(base) && !strings.Contains(base, "/") {
		sep = `\`
	}
	return strings.TrimRight(base, `/\`) + sep + rel
}

// IsAbsClientPath reports whether p is absolute in either a POSIX or a Windows
// download client's namespace.
func IsAbsClientPath(p string) bool {
	p = strings.TrimSpace(p)
	return strings.HasPrefix(p, "/") || IsWindowsPath(p)
}
