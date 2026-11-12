package desktop

import (
	"strings"

	"golang.org/x/sys/windows/registry"
)

// SystemManufacturerModel 读注册表取 OEM 厂商/型号(配对设备列表展示用, 与安卓 Build.MANUFACTURER/MODEL 对齐)。
// 双路径兜底: Control\System 是 Win10+ 标准位置, BIOS 路径覆盖老机; 取不到返回空串。
func SystemManufacturerModel() (manufacturer, model string) {
	for _, s := range []struct{ path, man, prod string }{
		{`SYSTEM\CurrentControlSet\Control\System`, "SystemManufacturer", "SystemProductName"},
		{`HARDWARE\DESCRIPTION\System\BIOS`, "SystemManufacturer", "SystemProductName"},
	} {
		k, err := registry.OpenKey(registry.LOCAL_MACHINE, s.path, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		manV, _, _ := k.GetStringValue(s.man)
		prodV, _, _ := k.GetStringValue(s.prod)
		_ = k.Close()
		if manV != "" || prodV != "" {
			return strings.TrimSpace(manV), strings.TrimSpace(prodV)
		}
	}
	return "", ""
}
