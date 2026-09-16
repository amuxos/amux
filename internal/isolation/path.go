package isolation

import (
	"os"
	"path/filepath"
	"strings"
)

func pathWithin(root, path string) bool {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}
