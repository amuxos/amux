package runtime

import (
	"github.com/amuxos/amux/internal/i18n"
	"os"
)

var maintenanceCatalog = func() *i18n.Catalog {
	c, err := i18n.New([]byte(`{"unavailable": {"other": "Runtime is preparing an update; retry after maintenance finishes."}, "journal": {"other": "Invalid upgrade recovery journal; repair it before starting the Runtime."}, "local": {"other": "Upgrade maintenance requires local administration access."}, "token": {"other": "Invalid upgrade maintenance token."}, "held": {"other": "Another update owns the maintenance lease. Wait for it to finish."}, "action": {"other": "Invalid upgrade maintenance action."}}`), []byte(`{"unavailable": {"other": "Runtime 正在准备升级，请在维护完成后重试。"}, "journal": {"other": "升级恢复记录无效，请修复后再启动 Runtime。"}, "local": {"other": "升级维护需要本机管理权限。"}, "token": {"other": "升级维护令牌无效。"}, "held": {"other": "另一升级进程持有维护租约，请等待其完成。"}, "action": {"other": "升级维护操作无效。"}}`))
	if err != nil {
		panic(err)
	}
	return c
}()

func maintenanceMessage(key string) string {
	return maintenanceCatalog.Format(i18n.Resolve(os.Getenv("AMUX_LANGUAGE"), "", ""), key)
}
