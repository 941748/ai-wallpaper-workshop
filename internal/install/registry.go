package install

import (
	"fmt"

	"golang.org/x/sys/windows/registry"
)

// WriteUninstallKey 写 HKCU 卸载项, 使程序出现在 Windows 设置 > 应用 与"应用和功能"列表中。
// 无需管理员权限(写 HKCU)。UninstallString 指向已安装 exe 的 --uninstall 模式。
func WriteUninstallKey(exePath, installLocation, version string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, uninstallRegKey,
		registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()

	values := map[string]string{
		"DisplayName":     DisplayName,
		"DisplayVersion":  version,
		"Publisher":       "cybermesh.space",
		"DisplayIcon":     exePath,
		"InstallLocation": installLocation,
		"UninstallString": fmt.Sprintf(`"%s" --uninstall`, exePath),
	}
	for name, val := range values {
		if err := k.SetStringValue(name, val); err != nil {
			return err
		}
	}
	// 无"修改/修复"入口: 只保留卸载
	if err := k.SetDWordValue("NoModify", 1); err != nil {
		return err
	}
	return k.SetDWordValue("NoRepair", 1)
}

// RemoveUninstallKey 删除 HKCU 卸载项(不存在则忽略)。
func RemoveUninstallKey() {
	_ = registry.DeleteKey(registry.CURRENT_USER, uninstallRegKey)
}
