// Package agentpacks exposes Agent Packs embedded in the amux binary.
package agentpacks

import (
	"embed"
	"io/fs"
)

//go:embed builtin/dist/agent-pack.manifest.json
var embedded embed.FS

func Builtin() fs.FS {
	rooted, err := fs.Sub(embedded, "builtin")
	if err != nil {
		panic(err)
	}
	return rooted
}
