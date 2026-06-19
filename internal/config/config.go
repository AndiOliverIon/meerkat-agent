// Package config stores optional local agent configuration. These files are
// user-controlled state on the VPS, never cloud state.
package config

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/AndiOliverIon/meerkat-agent/internal/fileutil"
)

const mssqlFile = "mssql-inventory.json"
const mssqlKeyFile = "mssql-inventory.key"
const relayFile = "relay.json"
const maxMSSQLInventories = 25
const mssqlPasswordCipherVersion byte = 1

var mssqlInventoryMu sync.Mutex

var ErrMSSQLInventoryNotFound = errors.New("mssql inventory config not found")
var ErrRelayConfigNotFound = errors.New("relay config not found")

type MSSQLInventory struct {
	Container string    `json:"container"`
	Username  string    `json:"username"`
	Password  string    `json:"password"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type persistedMSSQLInventory struct {
	Container         string    `json:"container"`
	Username          string    `json:"username"`
	Password          string    `json:"password,omitempty"`
	PasswordEncrypted string    `json:"passwordEncrypted,omitempty"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

type MSSQLInventorySummary struct {
	Container string    `json:"container"`
	Username  string    `json:"username"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type RelayConfig struct {
	BackendURL          string     `json:"backendUrl"`
	ServerID            string     `json:"serverId"`
	UserProfileID       string     `json:"userProfileId"`
	RelayToken          string     `json:"relayToken"`
	RelayTokenExpiresAt *time.Time `json:"relayTokenExpiresAt,omitempty"`
	UpdatedAt           time.Time  `json:"updatedAt"`
}

func MSSQLInventoryPath(dir string) string {
	return filepath.Join(dir, mssqlFile)
}

func MSSQLInventoryKeyPath(dir string) string {
	return filepath.Join(dir, mssqlKeyFile)
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
	if cfg.RelayToken == "" {
		return errors.New("relay token is required")
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
	cfg.RelayToken = strings.TrimSpace(cfg.RelayToken)
}

func LoadMSSQLInventories(dir string) ([]MSSQLInventory, error) {
	data, err := os.ReadFile(MSSQLInventoryPath(dir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var persisted []persistedMSSQLInventory
	if err := json.Unmarshal(data, &persisted); err != nil {
		return nil, err
	}
	configs := make([]MSSQLInventory, 0, len(persisted))
	for _, cfg := range persisted {
		password := cfg.Password
		if cfg.PasswordEncrypted != "" {
			var err error
			password, err = decryptMSSQLPassword(dir, cfg.PasswordEncrypted)
			if err != nil {
				return nil, err
			}
		}
		configs = append(configs, MSSQLInventory{
			Container: cfg.Container,
			Username:  cfg.Username,
			Password:  password,
			UpdatedAt: cfg.UpdatedAt,
		})
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
	persisted := make([]persistedMSSQLInventory, 0, len(configs))
	for _, cfg := range configs {
		encrypted, err := encryptMSSQLPassword(dir, cfg.Password)
		if err != nil {
			return err
		}
		persisted = append(persisted, persistedMSSQLInventory{
			Container:         cfg.Container,
			Username:          cfg.Username,
			PasswordEncrypted: encrypted,
			UpdatedAt:         cfg.UpdatedAt,
		})
	}
	data, err := json.MarshalIndent(persisted, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return fileutil.WriteFilePreserveOwner(MSSQLInventoryPath(dir), data, 0o600)
}

func encryptMSSQLPassword(dir string, password string) (string, error) {
	aead, err := mssqlPasswordAEAD(dir, true)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ciphertext := aead.Seal(nil, nonce, []byte(password), []byte{mssqlPasswordCipherVersion})
	envelope := make([]byte, 1+len(nonce)+len(ciphertext))
	envelope[0] = mssqlPasswordCipherVersion
	copy(envelope[1:], nonce)
	copy(envelope[1+len(nonce):], ciphertext)
	return base64.RawURLEncoding.EncodeToString(envelope), nil
}

func decryptMSSQLPassword(dir string, encoded string) (string, error) {
	envelope, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return "", err
	}
	aead, err := mssqlPasswordAEAD(dir, false)
	if err != nil {
		return "", err
	}
	if len(envelope) < 1+aead.NonceSize() || envelope[0] != mssqlPasswordCipherVersion {
		return "", fmt.Errorf("invalid MSSQL password envelope")
	}
	nonceStart := 1
	nonceEnd := nonceStart + aead.NonceSize()
	plaintext, err := aead.Open(nil, envelope[nonceStart:nonceEnd], envelope[nonceEnd:], []byte{mssqlPasswordCipherVersion})
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

func mssqlPasswordAEAD(dir string, create bool) (cipher.AEAD, error) {
	key, err := loadMSSQLPasswordKey(dir, create)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func loadMSSQLPasswordKey(dir string, create bool) ([]byte, error) {
	path := MSSQLInventoryKeyPath(dir)
	data, err := os.ReadFile(path)
	if err == nil {
		key, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(string(data)))
		if err != nil {
			return nil, err
		}
		if len(key) != 32 {
			return nil, fmt.Errorf("invalid MSSQL password key length")
		}
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) || !create {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	encoded := append([]byte(base64.RawURLEncoding.EncodeToString(key)), '\n')
	if err := fileutil.WriteFilePreserveOwner(path, encoded, 0o600); err != nil {
		return nil, err
	}
	return key, nil
}
