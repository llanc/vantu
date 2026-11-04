// Package update checks GitHub Releases for newer Vantu versions and performs
// in-place self-update for binary deployments.
package update

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	Repo          = "llanc/vantu"
	DockerImage   = "ghcr.io/llanc/vantu"
	cacheTTL      = 6 * time.Hour
	maxAssetBytes = 200 << 20
)

const (
	ModeBinary = "binary"
	ModeDocker = "docker"
)

// Info describes the current and latest versions plus how this instance can be upgraded.
type Info struct {
	CurrentVersion string    `json:"current_version"`
	LatestVersion  string    `json:"latest_version"`
	HasUpdate      bool      `json:"has_update"`
	ReleaseURL     string    `json:"release_url"`
	ReleaseNotes   string    `json:"release_notes"`
	PublishedAt    time.Time `json:"published_at,omitzero"`
	CheckedAt      time.Time `json:"checked_at,omitzero"`
	CheckError     string    `json:"check_error,omitempty"`
	InstallMode    string    `json:"install_mode"`
	CanSelfUpdate  bool      `json:"can_self_update"`
	SelfUpdateNote string    `json:"self_update_note,omitempty"`
	UpgradeCommand string    `json:"upgrade_command,omitempty"`
}

type release struct {
	TagName     string    `json:"tag_name"`
	HTMLURL     string    `json:"html_url"`
	Body        string    `json:"body"`
	PublishedAt time.Time `json:"published_at"`
	Assets      []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

// Manager caches release information and serializes self-update attempts.
type Manager struct {
	current string
	enabled bool
	apiBase string
	client  *http.Client

	mu       sync.Mutex
	latest   *release
	checked  time.Time
	checkErr string
	applying bool
}

// githubAPI can be overridden at link time (-X) to test against a mock release server.
var githubAPI = "https://api.github.com"

func NewManager(currentVersion string, enabled bool) *Manager {
	return &Manager{
		current: currentVersion,
		enabled: enabled,
		apiBase: githubAPI,
		client:  &http.Client{Timeout: 5 * time.Minute},
	}
}

// Info returns cached update information, refreshing it from GitHub when stale or forced.
func (m *Manager) Info(ctx context.Context, force bool) Info {
	m.mu.Lock()
	stale := m.checked.IsZero() || time.Since(m.checked) > cacheTTL
	m.mu.Unlock()
	if m.enabled && (force || stale) {
		rel, err := m.fetchLatest(ctx)
		m.mu.Lock()
		m.checked = time.Now().UTC()
		if err != nil {
			m.checkErr = err.Error()
		} else {
			m.latest, m.checkErr = rel, ""
		}
		m.mu.Unlock()
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	info := Info{
		CurrentVersion: m.current,
		CheckedAt:      m.checked,
		CheckError:     m.checkErr,
		InstallMode:    detectMode(),
	}
	if !m.enabled {
		info.CheckError = "已通过 VANTU_UPDATE_CHECK=off 关闭更新检查"
	}
	if m.latest != nil {
		info.LatestVersion = m.latest.TagName
		info.ReleaseURL = m.latest.HTMLURL
		info.ReleaseNotes = m.latest.Body
		info.PublishedAt = m.latest.PublishedAt
		info.HasUpdate = newer(m.latest.TagName, m.current)
	}
	if info.InstallMode == ModeDocker {
		info.SelfUpdateNote = "容器部署请拉取新镜像后重建容器"
		info.UpgradeCommand = "docker compose pull vantu && docker compose up -d vantu"
	} else if note := selfUpdateBlocker(); note != "" {
		info.SelfUpdateNote = note
	} else {
		info.CanSelfUpdate = true
	}
	return info
}

func (m *Manager) fetchLatest(ctx context.Context) (*release, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.apiBase+"/repos/"+Repo+"/releases/latest", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "vantu/"+m.current)
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("无法连接 GitHub: %w", err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, errors.New("未在 GitHub 找到可用的正式发布版本（仓库未公开或尚未发布）")
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests:
		return nil, errors.New("GitHub API 请求频率受限，请稍后再试")
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("GitHub API 返回 HTTP %d", resp.StatusCode)
	}
	var rel release
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&rel); err != nil {
		return nil, fmt.Errorf("解析发布信息失败: %w", err)
	}
	return &rel, nil
}

// Apply downloads the latest release for this platform, verifies its checksum
// and atomically replaces the running executable. The caller must restart the
// process afterwards for the new version to take effect.
func (m *Manager) Apply(ctx context.Context) (string, error) {
	m.mu.Lock()
	if m.applying {
		m.mu.Unlock()
		return "", errors.New("已有更新任务正在进行")
	}
	m.applying = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.applying = false
		m.mu.Unlock()
	}()

	info := m.Info(ctx, true)
	if !info.CanSelfUpdate {
		return "", errors.New(info.SelfUpdateNote)
	}
	if info.CheckError != "" {
		return "", errors.New(info.CheckError)
	}
	if !info.HasUpdate {
		return "", fmt.Errorf("当前已是最新版本 %s", info.CurrentVersion)
	}

	m.mu.Lock()
	rel := m.latest
	m.mu.Unlock()

	assetName := fmt.Sprintf("vantu_%s_%s_%s.tar.gz", rel.TagName, runtime.GOOS, runtime.GOARCH)
	var assetURL, sumsURL string
	for _, a := range rel.Assets {
		switch a.Name {
		case assetName:
			assetURL = a.URL
		case "checksums.txt":
			sumsURL = a.URL
		}
	}
	if assetURL == "" || sumsURL == "" {
		return "", fmt.Errorf("发布 %s 中缺少 %s 或 checksums.txt", rel.TagName, assetName)
	}

	sums, err := m.download(ctx, sumsURL, 1<<20)
	if err != nil {
		return "", fmt.Errorf("下载校验文件失败: %w", err)
	}
	wantSum, err := lookupChecksum(sums, assetName)
	if err != nil {
		return "", err
	}
	archive, err := m.download(ctx, assetURL, maxAssetBytes)
	if err != nil {
		return "", fmt.Errorf("下载 %s 失败: %w", assetName, err)
	}
	gotSum := sha256.Sum256(archive)
	if hex.EncodeToString(gotSum[:]) != wantSum {
		return "", fmt.Errorf("%s 校验和不匹配，已中止更新", assetName)
	}
	bin, err := extractBinary(archive)
	if err != nil {
		return "", err
	}
	exe, err := executablePath()
	if err != nil {
		return "", err
	}
	if err := replaceFile(exe, bin); err != nil {
		return "", fmt.Errorf("替换可执行文件失败: %w", err)
	}
	return rel.TagName, nil
}

func (m *Manager) download(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "vantu/"+m.current)
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("文件超出大小限制")
	}
	return data, nil
}

func lookupChecksum(sums []byte, name string) (string, error) {
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == name {
			return strings.ToLower(fields[0]), nil
		}
	}
	return "", fmt.Errorf("checksums.txt 中未找到 %s", name)
}

func extractBinary(archive []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("解压失败: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, errors.New("发布包中未找到 vantu 可执行文件")
		}
		if err != nil {
			return nil, fmt.Errorf("读取发布包失败: %w", err)
		}
		if hdr.Typeflag == tar.TypeReg && filepath.Base(hdr.Name) == "vantu" {
			return io.ReadAll(io.LimitReader(tr, maxAssetBytes))
		}
	}
}

var executablePath = func() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}

// replaceFile writes data next to dst and renames it over dst so the swap is atomic.
func replaceFile(dst string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".vantu-update-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		return err
	}
	return os.Rename(tmpName, dst)
}

func detectMode() string {
	for _, marker := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(marker); err == nil {
			return ModeDocker
		}
	}
	return ModeBinary
}

func selfUpdateBlocker() string {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return "当前平台暂不支持一键更新"
	}
	exe, err := executablePath()
	if err != nil {
		return "无法定位当前可执行文件"
	}
	probe, err := os.CreateTemp(filepath.Dir(exe), ".vantu-probe-*")
	if err != nil {
		return fmt.Sprintf("当前进程无权写入 %s，请以安装时的用户运行或手动替换二进制", filepath.Dir(exe))
	}
	probe.Close()
	os.Remove(probe.Name())
	return ""
}

// newer reports whether version a is strictly greater than b (vMAJOR.MINOR.PATCH).
func newer(a, b string) bool {
	pa, okA := parseSemver(a)
	pb, okB := parseSemver(b)
	if !okA || !okB {
		return false
	}
	for i := range pa {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return false
}

func parseSemver(v string) ([3]int, bool) {
	var out [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}
