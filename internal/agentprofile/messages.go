package agentprofile

import (
	_ "embed"
	"errors"

	"github.com/amuxos/amux/internal/i18n"
)

//go:embed locales/en.json
var englishMessages []byte

//go:embed locales/zh-CN.json
var chineseMessages []byte

var profileCatalog = func() *i18n.Catalog {
	catalog, err := i18n.New(englishMessages, chineseMessages)
	if err != nil {
		panic(err)
	}
	return catalog
}()

// Profile errors cross CLI, RPC and agent surfaces without a viewer locale.
// Keep both translations, without consulting process-global language settings.
func profileError(key string, args ...any) error {
	text := profileCatalog.Text(key, args...)
	return errors.New(text.For(i18n.English) + "\n" + text.For(i18n.Chinese))
}
