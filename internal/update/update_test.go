package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNewer(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"v0.2.0", "v0.1.0", true},
		{"v0.1.1", "v0.1.0", true},
		{"v1.0.0", "v0.9.9", true},
		{"v0.1.0", "v0.1.0", false},
		{"v0.1.0", "v0.2.0", false},
		{"v0.2.0", "v0.1.0-3-gabcdef-dirty", true},
		{"v0.1.0", "v0.1.0-3-gabcdef", false},
		{"v0.2.0", "dev", false},
	}
	for _, c := range cases {
		if got := newer(c.a, c.b); got != c.want {
			t.Errorf("newer(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestLookupChecksum(t *testing.T) {
	sums := []byte("abc123  vantu_v0.2.0_linux_amd64.tar.gz\nDEF456  vantu_v0.2.0_linux_arm64.tar.gz\n")
	got, err := lookupChecksum(sums, "vantu_v0.2.0_linux_arm64.tar.gz")
	if err != nil || got != "def456" {
		t.Fatalf("lookupChecksum = %q, %v", got, err)
	}
	if _, err := lookupChecksum(sums, "missing.tar.gz"); err == nil {
		t.Fatal("expected error for missing asset")
	}
}

func TestExtractBinary(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	files := map[string]string{
		"vantu_v0.2.0_linux_amd64/README.md": "readme",
		"vantu_v0.2.0_linux_amd64/vantu":     "ELF-binary",
	}
	for name, body := range files {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte(body))
	}
	_ = tw.Close()
	_ = gz.Close()

	bin, err := extractBinary(buf.Bytes())
	if err != nil || string(bin) != "ELF-binary" {
		t.Fatalf("extractBinary = %q, %v", bin, err)
	}
}

func TestReplaceFile(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "vantu")
	if err := os.WriteFile(dst, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := replaceFile(dst, []byte("new")); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(dst)
	st, _ := os.Stat(dst)
	if string(got) != "new" || st.Mode().Perm() != 0o755 {
		t.Fatalf("unexpected result: %q mode=%v", got, st.Mode())
	}
}

func buildArchive(t *testing.T, binBody string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "pkg/vantu", Mode: 0o755, Size: int64(len(binBody)), Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte(binBody))
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

func TestApply(t *testing.T) {
	if detectMode() == ModeDocker {
		t.Skip("self-update is disabled inside containers")
	}
	assetName := fmt.Sprintf("vantu_v0.2.0_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	archive := buildArchive(t, "new-binary")
	sum := sha256.Sum256(archive)
	goodSums := hex.EncodeToString(sum[:]) + "  " + assetName + "\n"
	sums := goodSums

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/" + Repo + "/releases/latest":
			fmt.Fprintf(w, `{"tag_name":"v0.2.0","assets":[{"name":%q,"browser_download_url":"%s/a"},{"name":"checksums.txt","browser_download_url":"%s/sums"}]}`,
				assetName, srv.URL, srv.URL)
		case "/a":
			_, _ = w.Write(archive)
		case "/sums":
			_, _ = w.Write([]byte(sums))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	exe := filepath.Join(t.TempDir(), "vantu")
	if err := os.WriteFile(exe, []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	orig := executablePath
	executablePath = func() (string, error) { return exe, nil }
	defer func() { executablePath = orig }()

	m := NewManager("v0.1.0", true)
	m.apiBase = srv.URL

	sums = strings.Repeat("0", 64) + "  " + assetName + "\n"
	if _, err := m.Apply(context.Background()); err == nil || !strings.Contains(err.Error(), "校验和不匹配") {
		t.Fatalf("expected checksum mismatch, got %v", err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "old-binary" {
		t.Fatalf("binary must stay untouched on checksum mismatch, got %q", got)
	}

	sums = goodSums
	to, err := m.Apply(context.Background())
	if err != nil || to != "v0.2.0" {
		t.Fatalf("Apply = %q, %v", to, err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "new-binary" {
		t.Fatalf("binary not replaced, got %q", got)
	}

	current := NewManager("v0.2.0", true)
	current.apiBase = srv.URL
	if _, err := current.Apply(context.Background()); err == nil {
		t.Fatal("expected Apply to refuse when already up to date")
	}
}

func TestInfoFromGitHub(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/"+Repo+"/releases/latest" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"tag_name":"v0.2.0","html_url":"https://example/rel","body":"notes","assets":[]}`))
	}))
	defer srv.Close()

	m := NewManager("v0.1.0", true)
	m.apiBase = srv.URL
	info := m.Info(context.Background(), true)
	if info.CheckError != "" || !info.HasUpdate || info.LatestVersion != "v0.2.0" || info.ReleaseNotes != "notes" {
		t.Fatalf("unexpected info: %+v", info)
	}

	disabled := NewManager("v0.1.0", false)
	disabled.apiBase = srv.URL
	if info := disabled.Info(context.Background(), true); info.HasUpdate || info.CheckError == "" {
		t.Fatalf("disabled manager should not check: %+v", info)
	}
}
