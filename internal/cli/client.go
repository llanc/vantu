package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type savedCredentials struct {
	Endpoint string `json:"endpoint"`
	Token    string `json:"token"`
}

type apiError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	Suggestion string `json:"suggestion,omitempty"`
}

type rawEnvelope struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data,omitempty"`
	Error *apiError       `json:"error,omitempty"`
	Meta  map[string]any  `json:"meta,omitempty"`
}

// CLIExitError wraps a deterministic process exit code without printing duplicate cobra usage.
type CLIExitError struct {
	ExitCode int
	Err      error
}

func (e *CLIExitError) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return fmt.Sprintf("exit code %d", e.ExitCode)
}

type apiClient struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

func newAPIClient(flagEndpoint, flagToken, flagSock string) *apiClient {
	cred := loadCredentials()

	endpoint := strings.TrimSpace(flagEndpoint)
	if endpoint == "" {
		endpoint = strings.TrimSpace(os.Getenv("VANTU_ENDPOINT"))
	}
	token := strings.TrimSpace(flagToken)
	if token == "" {
		token = strings.TrimSpace(os.Getenv("VANTU_TOKEN"))
	}
	if token == "" {
		token = cred.Token
	}

	// Prefer zero-config local Unix socket when endpoint is not explicitly remote.
	if endpoint == "" {
		sockCandidates := []string{
			strings.TrimSpace(flagSock),
			strings.TrimSpace(os.Getenv("VANTU_SOCK_PATH")),
			"./data/vantu.sock",
			"/data/vantu.sock",
		}
		for _, sp := range sockCandidates {
			if sp == "" {
				continue
			}
			if st, err := os.Stat(sp); err == nil && !st.IsDir() {
				sockPath := sp
				tr := &http.Transport{
					DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
						var d net.Dialer
						return d.DialContext(ctx, "unix", sockPath)
					},
				}
				return &apiClient{
					baseURL:    "http://unix",
					token:      token,
					httpClient: &http.Client{Transport: tr, Timeout: 65 * time.Second},
				}
			}
		}
		if cred.Endpoint != "" {
			endpoint = cred.Endpoint
		} else {
			endpoint = "http://127.0.0.1:8089"
		}
	}

	return &apiClient{
		baseURL:    strings.TrimRight(endpoint, "/"),
		token:      token,
		httpClient: &http.Client{Timeout: 65 * time.Second},
	}
}

func (c *apiClient) call(ctx context.Context, method, path string, body any) (*rawEnvelope, []byte, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
	if err != nil {
		return nil, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("无法连接到小凡兔守护进程 (%s): %w（请确认 vantu serve 正在运行）", c.baseURL, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, err
	}

	var env rawEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, raw, fmt.Errorf("解析服务端响应失败 (HTTP %d): %s", resp.StatusCode, string(raw))
	}
	return &env, raw, nil
}

func mapCodeToExit(code string) int {
	switch code {
	case "VALIDATION_ERROR", "NOT_FOUND":
		return 2
	case "AUTH_REQUIRED", "RATE_LIMITED":
		return 3
	case "DNS_ERROR", "DNS_CONFLICT":
		return 4
	case "EDGE_UNREACHABLE":
		return 5
	case "LOCAL_TARGET_DOWN":
		return 6
	case "TLS_TIMEOUT":
		return 7
	default:
		return 1
	}
}

func credentialsPath() string {
	cfgDir, err := os.UserConfigDir()
	if err != nil || cfgDir == "" {
		home, _ := os.UserHomeDir()
		cfgDir = filepath.Join(home, ".config")
	}
	return filepath.Join(cfgDir, "vantu", "credentials.json")
}

func saveCredentials(endpoint, token string) error {
	p := credentialsPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(savedCredentials{
		Endpoint: strings.TrimRight(endpoint, "/"),
		Token:    token,
	}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, append(b, '\n'), 0o600)
}

func loadCredentials() savedCredentials {
	var out savedCredentials
	if b, err := os.ReadFile(credentialsPath()); err == nil {
		_ = json.Unmarshal(b, &out)
	}
	return out
}
