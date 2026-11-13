package config

import (
	"fmt"
	"os"
	"path/filepath"
)

func rejectSymlinks(p string) error {
	absolute, err := filepath.Abs(p)
	if err != nil {
		return err
	}
	for current := absolute; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink destination forbidden")
		}
		if current == filepath.Dir(current) {
			return nil
		}
	}
}
