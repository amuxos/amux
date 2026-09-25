package modelservice

import (
	_ "embed"

	"github.com/amuxos/amux/internal/i18n"
)

//go:embed locales/en.json
var englishMessages []byte

//go:embed locales/zh-CN.json
var chineseMessages []byte

var errorCatalog = func() *i18n.Catalog {
	catalog, err := i18n.New(englishMessages, chineseMessages)
	if err != nil {
		panic(err)
	}
	return catalog
}()

// The agent protocol has no viewer locale. Keep diagnostics bilingual without
// consulting host language settings or changing another principal's output.
func errorMessage(key string) string {
	text := errorCatalog.Text(key)
	return text.For(i18n.English) + "\n" + text.For(i18n.Chinese)
}
