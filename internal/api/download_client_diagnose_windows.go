//go:build windows

package api

// diagDeviceKey groups every root together on Windows, where
// hardlinkableReason answers the same way for all of them without touching
// the filesystem.
func diagDeviceKey(_ string) string {
	return "windows"
}
