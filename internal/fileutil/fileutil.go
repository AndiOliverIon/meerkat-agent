// Package fileutil contains small helpers for writing agent-owned state files.
package fileutil

import (
	"os"
	"path/filepath"
	"syscall"
)

// WriteFilePreserveOwner atomically replaces path, applies perm explicitly, and
// keeps the existing owner when the file already exists. For new files, it uses
// the parent directory owner so sudo-run config commands still create files the
// service user can read.
func WriteFilePreserveOwner(path string, data []byte, perm os.FileMode) error {
	var uid, gid int
	preserveOwner := false
	if info, err := os.Stat(path); err == nil {
		if st, ok := info.Sys().(*syscall.Stat_t); ok {
			uid, gid = int(st.Uid), int(st.Gid)
			preserveOwner = true
		}
	} else if info, err := os.Stat(filepath.Dir(path)); err == nil {
		if st, ok := info.Sys().(*syscall.Stat_t); ok {
			uid, gid = int(st.Uid), int(st.Gid)
			preserveOwner = true
		}
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	if preserveOwner {
		if err := os.Chown(tmp, uid, gid); err != nil {
			_ = os.Remove(tmp)
			return err
		}
	}
	if err := os.Chmod(tmp, perm); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}
