//go:build !windows

package api

import (
	"strconv"
	"syscall"
)

// diagDeviceKey identifies the filesystem device a library root lives on, so
// the diagnose action runs one hardlink probe per device rather than one per
// root. A root that cannot be inspected gets a key of its own, so its probe
// runs and reports the error.
func diagDeviceKey(root string) string {
	info, err := statExistingDevice(root)
	if err != nil {
		return "unreadable:" + root
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "unknown:" + root
	}
	return "dev:" + strconv.FormatUint(uint64(st.Dev), 10)
}
