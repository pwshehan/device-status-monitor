//go:build !windows

package probe

// platformClass has nothing to add off Windows: the POSIX errno checks in
// classify cover every case there.
func platformClass(error) (Class, bool) { return "", false }
