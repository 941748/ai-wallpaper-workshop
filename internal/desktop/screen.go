package desktop

// PrimarySize 返回主显示器物理分辨率(像素)。
func PrimarySize() (int, int) {
	w, _, _ := procGetSystemMetrics.Call(smCXScreen)
	h, _, _ := procGetSystemMetrics.Call(smCYScreen)
	return int(w), int(h)
}
