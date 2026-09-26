//go:build !windows

package winsvc

// GrantSystemAccess is a no-op off Windows.
func GrantSystemAccess(string) error { return nil }
