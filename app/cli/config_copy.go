package cli

import (
	_ "embed"
	"os"

	"github.com/amuxos/amux/internal/i18n"
	"github.com/spf13/cobra"
)

//go:embed locales/en.json
var commandEnglish []byte

//go:embed locales/zh-CN.json
var commandChinese []byte

var commandCatalog = func() *i18n.Catalog {
	c, err := i18n.New(commandEnglish, commandChinese)
	if err != nil {
		panic(err)
	}
	return c
}()

func commandLocale(cmd *cobra.Command) i18n.Locale {
	if jsonOutputEnabled(cmd) {
		return i18n.English
	}
	client := ""
	for _, key := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if value := os.Getenv(key); value != "" {
			client = value
			break
		}
	}
	// POSIX locale encoding suffixes are not language tags.
	for i, c := range client {
		if c == '.' || c == '@' {
			client = client[:i]
			break
		}
	}
	return i18n.Resolve(os.Getenv("AMUX_LANGUAGE"), client, "")
}
