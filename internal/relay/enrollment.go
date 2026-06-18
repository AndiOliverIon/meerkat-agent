package relay

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	agentconfig "github.com/AndiOliverIon/meerkat-agent/internal/config"
)

type EnrollmentCode struct {
	BackendURL string `json:"backendUrl"`
	Code       string `json:"code"`
}

func DecodeEnrollmentCode(encoded string) (EnrollmentCode, error) {
	data, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return EnrollmentCode{}, err
	}
	var code EnrollmentCode
	if err := json.Unmarshal(data, &code); err != nil {
		return EnrollmentCode{}, err
	}
	code.BackendURL = strings.TrimRight(strings.TrimSpace(code.BackendURL), "/")
	code.Code = strings.TrimSpace(code.Code)
	if code.BackendURL == "" {
		return EnrollmentCode{}, errors.New("backend url is required")
	}
	if code.Code == "" {
		return EnrollmentCode{}, errors.New("enrollment code is required")
	}
	return code, nil
}

func ConsumeEnrollmentCode(ctx context.Context, encoded string, fingerprint string, agentVersion string, client *http.Client) (agentconfig.RelayConfig, error) {
	code, err := DecodeEnrollmentCode(encoded)
	if err != nil {
		return agentconfig.RelayConfig{}, err
	}
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	endpoint, err := joinPath(code.BackendURL, "/v1/agent/relay-enroll")
	if err != nil {
		return agentconfig.RelayConfig{}, err
	}
	body, err := json.Marshal(struct {
		EnrollmentCode string `json:"enrollmentCode"`
		Fingerprint    string `json:"fingerprint,omitempty"`
		AgentVersion   string `json:"agentVersion,omitempty"`
	}{
		EnrollmentCode: strings.TrimSpace(encoded),
		Fingerprint:    strings.TrimSpace(fingerprint),
		AgentVersion:   strings.TrimSpace(agentVersion),
	})
	if err != nil {
		return agentconfig.RelayConfig{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return agentconfig.RelayConfig{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return agentconfig.RelayConfig{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return agentconfig.RelayConfig{}, fmt.Errorf("relay enrollment status %d", resp.StatusCode)
	}
	var payload struct {
		BackendURL    string `json:"backendUrl"`
		ServerID      string `json:"serverId"`
		UserProfileID string `json:"userProfileId"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&payload); err != nil {
		return agentconfig.RelayConfig{}, err
	}
	cfg := agentconfig.RelayConfig{
		BackendURL:    strings.TrimRight(strings.TrimSpace(payload.BackendURL), "/"),
		ServerID:      strings.TrimSpace(payload.ServerID),
		UserProfileID: strings.TrimSpace(payload.UserProfileID),
	}
	if cfg.BackendURL == "" {
		cfg.BackendURL = code.BackendURL
	}
	return cfg, nil
}
