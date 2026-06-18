package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/AndiOliverIon/meerkat-agent/internal/collect"
)

const (
	SnapshotIntervalKey     = "agent.snapshot_push_interval_seconds"
	SettingsRefreshEveryKey = "agent.settings_refresh_every_pushes"
)

type Settings struct {
	SnapshotPushInterval    time.Duration
	SettingsRefreshEvery    int
	LatestSnapshotRetention time.Duration
	Raw                     map[string]string
}

func DefaultSettings() Settings {
	return Settings{
		SnapshotPushInterval: time.Minute,
		SettingsRefreshEvery: 5,
		Raw: map[string]string{
			SnapshotIntervalKey:     "60",
			SettingsRefreshEveryKey: "5",
		},
	}
}

func SettingsFromMap(values map[string]string, fallback Settings) Settings {
	next := fallback
	if next.Raw == nil {
		next.Raw = map[string]string{}
	}
	for key, value := range values {
		next.Raw[key] = value
	}
	if seconds, ok := positiveInt(values[SnapshotIntervalKey]); ok {
		next.SnapshotPushInterval = time.Duration(seconds) * time.Second
	}
	if pushes, ok := positiveInt(values[SettingsRefreshEveryKey]); ok {
		next.SettingsRefreshEvery = pushes
	}
	return next
}

type Runner struct {
	BackendURL    string
	ServerID      string
	UserProfileID string
	Collector     *collect.Collector
	Client        *http.Client
	Logger        *log.Logger
}

func (r Runner) Run(ctx context.Context) error {
	if strings.TrimSpace(r.BackendURL) == "" {
		return errors.New("backend url is required")
	}
	if strings.TrimSpace(r.ServerID) == "" {
		return errors.New("server id is required")
	}
	if strings.TrimSpace(r.UserProfileID) == "" {
		return errors.New("user profile id is required")
	}
	collector := r.Collector
	if collector == nil {
		collector = collect.New()
	}
	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	logger := r.Logger
	if logger == nil {
		logger = log.Default()
	}

	settings := DefaultSettings()
	pushesSinceSettings := settings.SettingsRefreshEvery
	for {
		if pushesSinceSettings >= settings.SettingsRefreshEvery {
			if remote, err := r.fetchSettings(ctx, client); err != nil {
				logger.Printf("meerkat-agent relay settings: %v", err)
			} else {
				settings = SettingsFromMap(remote, settings)
				logger.Printf("meerkat-agent relay settings: snapshot interval=%s refreshEvery=%d", settings.SnapshotPushInterval, settings.SettingsRefreshEvery)
			}
			pushesSinceSettings = 0
		}

		if err := r.pushSnapshot(ctx, client, collector); err != nil {
			logger.Printf("meerkat-agent relay push: %v", err)
		} else {
			logger.Print("meerkat-agent relay push: uploaded latest snapshot")
		}
		pushesSinceSettings++

		timer := time.NewTimer(settings.SnapshotPushInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

func (r Runner) fetchSettings(ctx context.Context, client *http.Client) (map[string]string, error) {
	endpoint, err := joinPath(r.BackendURL, "/v1/agent/settings")
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("settings status %d", resp.StatusCode)
	}
	var payload struct {
		Settings map[string]string `json:"settings"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&payload); err != nil {
		return nil, err
	}
	if payload.Settings == nil {
		payload.Settings = map[string]string{}
	}
	return payload.Settings, nil
}

func (r Runner) pushSnapshot(ctx context.Context, client *http.Client, collector *collect.Collector) error {
	snapshot := collector.Snapshot()
	body, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	endpoint, err := joinPath(r.BackendURL, "/v1/servers/"+url.PathEscape(r.ServerID)+"/snapshot")
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Meerkat-User-ID", r.UserProfileID)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("snapshot status %d", resp.StatusCode)
	}
	return nil
}

func positiveInt(value string) (int, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return 0, false
	}
	return parsed, true
}

func joinPath(base, path string) (string, error) {
	parsed, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil {
		return "", err
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", errors.New("backend url must include scheme and host")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + path
	return parsed.String(), nil
}
