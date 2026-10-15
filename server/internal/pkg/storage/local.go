package storage

import (
	"io"
	"os"
	"path/filepath"
)

type LocalStorage struct {
	basePath string
}

func NewLocalStorage(basePath string) *LocalStorage {
	return &LocalStorage{basePath: basePath}
}

func (s *LocalStorage) Save(path string, reader io.Reader) error {
	fullPath := filepath.Join(s.basePath, path)
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return err
	}

	file, err := os.Create(fullPath)
	if err != nil {
		return err
	}
	defer file.Close()

	_, err = io.Copy(file, reader)
	return err
}

func (s *LocalStorage) Delete(path string) error {
	fullPath := filepath.Join(s.basePath, path)
	return os.Remove(fullPath)
}

// Rename 用 os.Rename 完成同根目录内的原子改名；跨设备场景不存在——
// 两个路径都解析自同一 basePath。
func (s *LocalStorage) Rename(oldPath, newPath string) error {
	from := filepath.Join(s.basePath, oldPath)
	to := filepath.Join(s.basePath, newPath)
	if err := os.MkdirAll(filepath.Dir(to), 0750); err != nil {
		return err
	}
	return os.Rename(from, to)
}

func (s *LocalStorage) DeletePrefix(path string) error {
	fullPath := filepath.Join(s.basePath, path)
	return os.RemoveAll(fullPath)
}

func (s *LocalStorage) Get(path string) (io.ReadCloser, error) {
	fullPath := filepath.Join(s.basePath, path)
	return os.Open(fullPath)
}

func (s *LocalStorage) Exists(path string) (bool, error) {
	fullPath := filepath.Join(s.basePath, path)
	_, err := os.Stat(fullPath)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}
