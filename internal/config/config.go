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
	"time"

	"github.com/AndiOliverIon/meerkat-agent/internal/fileutil"
)

const mssqlFile = "mssql-inventory.json"
const relayFile = "relay.json"
const maxMSSQLInventories = 25

var mssqlInventoryMu sync.Mutex

var ErrMSSQLInventoryNotFound = errors.New("mssql inventory config not found")
var ErrRelayConfigNotFound = errors.New("relay config not found")

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

type RelayConfig struct {
	BackendURL    string    `json:"backendUrl"`
	ServerID      string    `json:"serverId"`
	UserProfileID string    `json:"userProfileId"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

func MSSQLInventoryPath(dir string) string {
	return filepath.Join(dir, mssqlFile)
}

func RelayConfigPath(dir string) string {
	return filepath.Join(dir, relayFile)
}

func LoadRelayConfig(dir string) (RelayConfig, error) {
	data, err := os.ReadFile(RelayConfigPath(dir))
	if errors.Is(err, os.ErrNotExist) {
		return RelayConfig{}, ErrRelayConfigNotFound
	}
	if err != nil {
		return RelayConfig{}, err
	}
	var cfg RelayConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return RelayConfig{}, err
	}
	cfg.trim()
	return cfg, nil
}

func SaveRelayConfig(dir string, cfg RelayConfig) error {
	cfg.trim()
	if cfg.BackendURL == "" {
		return errors.New("backend url is required")
	}
	if cfg.ServerID == "" {
		return errors.New("server id is required")
	}
	if cfg.UserProfileID == "" {
		return errors.New("user profile id is required")
	}
	cfg.UpdatedAt = time.Now().UTC()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return fileutil.WriteFilePreserveOwner(RelayConfigPath(dir), data, 0o600)
}

func RemoveRelayConfig(dir string) error {
	if err := os.Remove(RelayConfigPath(dir)); errors.Is(err, os.ErrNotExist) {
		return ErrRelayConfigNotFound
	} else if err != nil {
		return err
	}
	return nil
}

func (cfg *RelayConfig) trim() {
	cfg.BackendURL = strings.TrimSpace(cfg.BackendURL)
	cfg.ServerID = strings.TrimSpace(cfg.ServerID)
	cfg.UserProfileID = strings.TrimSpace(cfg.UserProfileID)
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

	return writeMSSQLInventories(dir, configs)
}

func RemoveMSSQLInventory(dir, container string) error {
	mssqlInventoryMu.Lock()
	defer mssqlInventoryMu.Unlock()

	container = strings.TrimSpace(container)
	if container == "" {
		return errors.New("container is required")
	}

	configs, err := LoadMSSQLInventories(dir)
	if err != nil {
		return err
	}

	next := configs[:0]
	removed := false
	for _, cfg := range configs {
		if cfg.Container == container {
			removed = true
			continue
		}
		next = append(next, cfg)
	}
	if !removed {
		return ErrMSSQLInventoryNotFound
	}
	if len(next) == 0 {
		if err := os.Remove(MSSQLInventoryPath(dir)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return writeMSSQLInventories(dir, next)
}

func SummarizeMSSQLInventory(cfg MSSQLInventory) MSSQLInventorySummary {
	return MSSQLInventorySummary{
		Container: cfg.Container,
		Username:  cfg.Username,
		UpdatedAt: cfg.UpdatedAt,
	}
}

func writeMSSQLInventories(dir string, configs []MSSQLInventory) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(configs, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return fileutil.WriteFilePreserveOwner(MSSQLInventoryPath(dir), data, 0o600)
}
