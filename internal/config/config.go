// Package config stores optional local agent configuration. These files are
// user-controlled state on the VPS, never cloud state.
package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const mssqlFile = "mssql-inventory.json"
const maxMSSQLInventories = 25

var mssqlInventoryMu sync.Mutex

type MSSQLInventory struct {
	Container string    `json:"container"`
	Username  string    `json:"username"`
	Password  string    `json:"password"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type MSSQLInventorySummary struct {
	Container string    `json:"container"`
	Username  string    `json:"username"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func MSSQLInventoryPath(dir string) string {
	return filepath.Join(dir, mssqlFile)
}

func LoadMSSQLInventories(dir string) ([]MSSQLInventory, error) {
	data, err := os.ReadFile(MSSQLInventoryPath(dir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var configs []MSSQLInventory
	if err := json.Unmarshal(data, &configs); err != nil {
		return nil, err
	}
	return configs, nil
}

func SaveMSSQLInventory(dir string, next MSSQLInventory) error {
	mssqlInventoryMu.Lock()
	defer mssqlInventoryMu.Unlock()

	next.Container = strings.TrimSpace(next.Container)
	next.Username = strings.TrimSpace(next.Username)
	if next.Container == "" {
		return errors.New("container is required")
	}
	if next.Username == "" {
		return errors.New("username is required")
	}
	if next.Password == "" {
		return errors.New("password is required")
	}
	next.UpdatedAt = time.Now().UTC()

	configs, err := LoadMSSQLInventories(dir)
	if err != nil {
		return err
	}
	replaced := false
	for i, cfg := range configs {
		if cfg.Container == next.Container {
			configs[i] = next
			replaced = true
			break
		}
	}
	if !replaced {
		if len(configs) >= maxMSSQLInventories {
			return errors.New("too many MSSQL inventory entries")
		}
		configs = append(configs, next)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(configs, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writeFilePreserveOwner(MSSQLInventoryPath(dir), data, 0o600)
}

func SummarizeMSSQLInventory(cfg MSSQLInventory) MSSQLInventorySummary {
	return MSSQLInventorySummary{
		Container: cfg.Container,
		Username:  cfg.Username,
		UpdatedAt: cfg.UpdatedAt,
	}
}

func writeFilePreserveOwner(path string, data []byte, perm os.FileMode) error {
	var uid, gid int
	preserveOwner := false
	if info, err := os.Stat(path); err == nil {
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
