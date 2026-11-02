package config

import (
	"errors"
	"github.com/amuxos/amux/internal/i18n"
)

var executableCatalog = func() *i18n.Catalog {
	catalog, err := i18n.New([]byte(`{
 "invalid":{"other":"Invalid executable setting %s: enter a program path of at most 4096 characters, without line breaks."},
 "home":{"other":"%s: execution HOME is required for a ~/ path."},
 "relative":{"other":"%s: use a program name, absolute path or ~/ path instead of a relative path."},
 "unsupported":{"other":"Native model discovery is not supported for %s."}
 }`), []byte(`{
 "invalid":{"other":"执行程序配置 %s 无效：请输入不超过 4096 字符且不含换行的程序路径。"},
 "home":{"other":"%s：解析 ~/ 路径需要执行用户的 HOME。"},
 "relative":{"other":"%s：请使用程序名、绝对路径或 ~/ 路径，不支持相对路径。"},
 "unsupported":{"other":"%s 不支持原生模型发现。"}
 }`))
	if err != nil {
		panic(err)
	}
	return catalog
}()

// ExecutableError crosses RPC and CLI boundaries without a viewer locale.
func ExecutableError(key string, args ...any) error {
	text := executableCatalog.Text(key, args...)
	return errors.New(text.For(i18n.English) + "\n" + text.For(i18n.Chinese))
}
