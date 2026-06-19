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
	maxPushFailureBackoff   = 15 * time.Minute
)

var ErrRelayReenrollmentRequired = errors.New("relay re-enrollment required")

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
	BackendURL          string
	ServerID            string
	UserProfileID       string
	RelayToken          string
	RelayTokenExpiresAt *time.Time
	Collector           *collect.Collector
	Client              *http.Client
	Logger              *log.Logger
	Now                 func() time.Time
}

func (r Runner) Run(ctx context.Context) error {
	if strings.TrimSpace(r.BackendURL) == "" {
		return errors.New("backend url is required")
	}
	if strings.TrimSpace(r.ServerID) == "" {
		return errors.New("server id is required")
	}
	if strings.TrimSpace(r.RelayToken) == "" {
		return errors.New("relay token is required")
	}
	now := r.Now
	if now == nil {
		now = time.Now
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
	pushFailureBackoff := time.Duration(0)
	for {
		if err := r.requireValidRelayToken(now()); err != nil {
			return err
		}
		if pushesSinceSettings >= settings.SettingsRefreshEvery {
			if remote, err := r.fetchSettings(ctx, client); err != nil {
				if errors.Is(err, ErrRelayReenrollmentRequired) {
					logger.Printf("meerkat-agent relay settings: relay token expired or rejected; re-enroll required")
					return err
				}
				logger.Printf("meerkat-agent relay settings: %v", err)
			} else {
				settings = SettingsFromMap(remote, settings)
				logger.Printf("meerkat-agent relay settings: snapshot interval=%s refreshEvery=%d", settings.SnapshotPushInterval, settings.SettingsRefreshEvery)
			}
			pushesSinceSettings = 0
		}

		if err := r.pushSnapshot(ctx, client, collector); err != nil {
			if errors.Is(err, ErrRelayReenrollmentRequired) {
				logger.Printf("meerkat-agent relay push: relay token expired or rejected; re-enroll required")
				return err
			}
			logger.Printf("meerkat-agent relay push: %v", err)
			var delay time.Duration
			delay, pushFailureBackoff = nextPushDelay(settings.SnapshotPushInterval, pushFailureBackoff, true)
			logger.Printf("meerkat-agent relay push: retrying in %s", delay)
			pushesSinceSettings++
			if !sleepContext(ctx, delay) {
				return nil
			}
			continue
		} else {
			_, pushFailureBackoff = nextPushDelay(settings.SnapshotPushInterval, pushFailureBackoff, false)
			logger.Print("meerkat-agent relay push: uploaded latest snapshot")
		}
		pushesSinceSettings++

		if !sleepContext(ctx, settings.SnapshotPushInterval) {
			return nil
		}
	}
}

func (r Runner) requireValidRelayToken(now time.Time) error {
	if r.RelayTokenExpiresAt == nil {
		return nil
	}
	if now.Before(*r.RelayTokenExpiresAt) {
		return nil
	}
	return fmt.Errorf("relay token expired at %s; run meerkat-agent config relay set --enrollment-code CODE to re-enroll", r.RelayTokenExpiresAt.UTC().Format(time.RFC3339))
}

func nextPushDelay(snapshotInterval time.Duration, currentBackoff time.Duration, failed bool) (time.Duration, time.Duration) {
	if !failed {
		return snapshotInterval, 0
	}
	next := currentBackoff * 2
	if next <= 0 {
		next = snapshotInterval
	}
	if next > maxPushFailureBackoff {
		next = maxPushFailureBackoff
	}
	return next, next
}

func sleepContext(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
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
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(r.RelayToken))
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("%w: settings status %d", ErrRelayReenrollmentRequired, resp.StatusCode)
	}
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
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(r.RelayToken))
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("%w: snapshot status %d", ErrRelayReenrollmentRequired, resp.StatusCode)
	}
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
