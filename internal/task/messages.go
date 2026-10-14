package task

import (
	_ "embed"
	"errors"

	"github.com/amuxos/amux/internal/i18n"
)

//go:embed locales/en.json
var englishMessages []byte

//go:embed locales/zh-CN.json
var chineseMessages []byte
var taskCatalog = func() *i18n.Catalog {
	catalog, err := i18n.New(englishMessages, chineseMessages)
	if err != nil {
		panic(err)
	}
	return catalog
}()

func taskPermissionResponseError(id string, err error) error {
	text := taskCatalog.Text("permission_response_failed", id, err.Error())
	return errors.New(text.For(i18n.English) + "\n" + text.For(i18n.Chinese))
}
