package storage

import "io"

// Storage 文件存储接口
type Storage interface {
	// Save 保存文件，path 为相对存储根目录的路径
	Save(path string, reader io.Reader) error
	// Rename 把 oldPath 原子改名到 newPath（同一存储根内）；目标存在则覆盖。
	// 覆盖写场景先 Save 到临时路径再 Rename 到正式路径，读者不会看到半截文件。
	Rename(oldPath, newPath string) error
	// Delete 删除文件
	Delete(path string) error
	// DeletePrefix 删除指定前缀下的所有文件
	DeletePrefix(path string) error
	// Get 获取文件读取器
	Get(path string) (io.ReadCloser, error)
	// Exists 检查文件是否存在
	Exists(path string) (bool, error)
}
