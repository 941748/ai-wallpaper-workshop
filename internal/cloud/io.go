package cloud

import "os"

// 文件读写辅助(供补传队列使用)。

func osReadFile(path string) ([]byte, error) { return os.ReadFile(path) }

func isNotExist(err error) bool { return os.IsNotExist(err) }

func atomicWrite(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
