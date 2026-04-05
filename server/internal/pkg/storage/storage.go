package storage

import "io"

// Storage 文件存储接口
type Storage interface {
	// Save 保存文件，path 为相对存储根目录的路径
	Save(path string, reader io.Reader) error
	// Delete 删除文件
	Delete(path string) error
	// Get 获取文件读取器
	Get(path string) (io.ReadCloser, error)
	// Exists 检查文件是否存在
	Exists(path string) (bool, error)
}
