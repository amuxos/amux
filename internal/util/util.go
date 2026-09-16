// Package util holds small, dependency-free helpers shared across internal
// packages. Keep this package free of amux-specific concepts so any internal
// package may import it without introducing cross-cutting coupling.
package util

// BoolInt converts a bool to the integer encoding used by SQLite columns
// (1 for true, 0 for false).
func BoolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
