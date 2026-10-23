package acp

import (
	_ "embed"
	"errors"

	"github.com/amuxos/amux/internal/i18n"
)

//go:embed locales/en.json
var englishMessages []byte

//go:embed locales/zh-CN.json
var chineseMessages []byte

var acpCatalog = func() *i18n.Catalog {
	catalog, err := i18n.New(englishMessages, chineseMessages)
	if err != nil {
		panic(err)
	}
	return catalog
}()

// ACP protocol diagnostics have no viewer locale. Keep both translations
// without consulting host language settings or changing another user's locale.
func acpError(key string, args ...any) error {
	text := acpCatalog.Text(key, args...)
	return errors.New(text.For(i18n.English) + "\n" + text.For(i18n.Chinese))
}

var ErrExecutionConfigurationUnsupported = acpError("execution.unsupported")
