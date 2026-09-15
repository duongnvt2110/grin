package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpgradeAlreadyCurrent(t *testing.T) {
	archiveRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/latest" {
			_, _ = w.Write([]byte(`{"tag_name":"v0.2.0"}`))
			return
		}
		archiveRequests++
		http.NotFound(w, r)
	}))
	defer server.Close()

	executable := writeFakeExecutable(t, []byte("old"))
	var output bytes.Buffer
	if err := runUpgrade(&output, "0.2.0", testUpgradeOptions(server, executable)); err != nil {
		t.Fatal(err)
	}
	if archiveRequests != 0 {
		t.Fatalf("unexpected release artifact requests: %d", archiveRequests)
	}
	if got := readFile(t, executable); string(got) != "old" {
		t.Fatalf("executable changed: %q", got)
	}
	if !strings.Contains(output.String(), "already up to date (0.2.0)") {
		t.Fatalf("upgrade output = %q", output.String())
	}
}

func TestUpgradeReplacesExecutableAfterChecksumVerification(t *testing.T) {
	archive := makeReleaseArchive(t, "grin", []byte("new-binary"))
	checksum := sha256.Sum256(archive)
	server := newReleaseServer(t, "v0.2.0", archive, fmt.Sprintf("%x  grin_Darwin_arm64.tar.gz\n", checksum))
	defer server.Close()

	executable := writeFakeExecutable(t, []byte("old-binary"))
	var output bytes.Buffer
	if err := runUpgrade(&output, "0.1.0", testUpgradeOptions(server, executable)); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, executable); string(got) != "new-binary" {
		t.Fatalf("upgraded executable = %q", got)
	}
	if !strings.Contains(output.String(), "Upgraded Grin 0.1.0 → 0.2.0") {
		t.Fatalf("upgrade output = %q", output.String())
	}
}

func TestUpgradeRefusesDevBuildWithoutNetwork(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		http.Error(w, "unexpected", http.StatusInternalServerError)
	}))
	defer server.Close()

	err := runUpgrade(&bytes.Buffer{}, "dev", testUpgradeOptions(server, writeFakeExecutable(t, []byte("old"))))
	if err == nil || !strings.Contains(err.Error(), "development builds cannot self-upgrade") {
		t.Fatalf("runUpgrade error = %v", err)
	}
	if called {
		t.Fatal("development upgrade unexpectedly used the network")
	}
}

func TestUpgradeRefusesDowngrade(t *testing.T) {
	artifactRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/latest" {
			_, _ = w.Write([]byte(`{"tag_name":"v0.2.0"}`))
			return
		}
		artifactRequests++
		http.NotFound(w, r)
	}))
	defer server.Close()

	executable := writeFakeExecutable(t, []byte("old"))
	err := runUpgrade(&bytes.Buffer{}, "0.3.0", testUpgradeOptions(server, executable))
	if err == nil || !strings.Contains(err.Error(), "refusing downgrade") {
		t.Fatalf("runUpgrade error = %v", err)
	}
	if artifactRequests != 0 {
		t.Fatalf("unexpected artifact requests: %d", artifactRequests)
	}
	if got := readFile(t, executable); string(got) != "old" {
		t.Fatalf("executable changed: %q", got)
	}
}

func TestUpgradeFailuresLeaveExecutableUntouched(t *testing.T) {
	tests := []struct {
		name      string
		handler   http.HandlerFunc
		wantError string
	}{
		{
			name: "latest request",
			handler: func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "fail", http.StatusInternalServerError)
			},
			wantError: "get latest release",
		},
		{
			name: "archive request",
			handler: func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/latest" {
					_, _ = w.Write([]byte(`{"tag_name":"v0.2.0"}`))
					return
				}
				http.NotFound(w, r)
			},
			wantError: "download grin_Darwin_arm64.tar.gz",
		},
		{
			name:      "checksum mismatch",
			handler:   releaseHandler(t, "v0.2.0", makeReleaseArchive(t, "grin", []byte("new")), strings.Repeat("0", 64)+"  grin_Darwin_arm64.tar.gz\n"),
			wantError: "checksum verification failed",
		},
		{
			name: "missing binary",
			handler: func() http.HandlerFunc {
				archive := makeReleaseArchive(t, "other", []byte("new"))
				sum := sha256.Sum256(archive)
				return releaseHandler(t, "v0.2.0", archive, fmt.Sprintf("%x  grin_Darwin_arm64.tar.gz\n", sum))
			}(),
			wantError: "did not contain the grin binary",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(test.handler)
			defer server.Close()
			executable := writeFakeExecutable(t, []byte("old"))
			err := runUpgrade(&bytes.Buffer{}, "0.1.0", testUpgradeOptions(server, executable))
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("runUpgrade error = %v, want %q", err, test.wantError)
			}
			if got := readFile(t, executable); string(got) != "old" {
				t.Fatalf("executable changed after failure: %q", got)
			}
		})
	}
}

func TestReleaseArchiveNameRejectsUnsupportedPlatform(t *testing.T) {
	if _, err := releaseArchiveName("windows", "amd64"); err == nil {
		t.Fatal("windows unexpectedly supported")
	}
	if _, err := releaseArchiveName("linux", "386"); err == nil {
		t.Fatal("386 unexpectedly supported")
	}
}

func TestReplaceExecutableFailureLeavesTarget(t *testing.T) {
	target := filepath.Join(t.TempDir(), "grin")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "keep"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := replaceExecutable(target, []byte("new")); err == nil {
		t.Fatal("replaceExecutable unexpectedly succeeded")
	}
	if got := readFile(t, filepath.Join(target, "keep")); string(got) != "old" {
		t.Fatalf("target changed after replacement failure: %q", got)
	}
}

func TestUpgradeCLIHelp(t *testing.T) {
	var output bytes.Buffer
	if err := runUpgradeCLI(&output, "dev", []string{"--help"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Usage: grin upgrade") {
		t.Fatalf("help output = %q", output.String())
	}
}

func testUpgradeOptions(server *httptest.Server, executable string) upgradeOptions {
	return upgradeOptions{
		LatestReleaseURL: server.URL + "/latest",
		DownloadBaseURL:  server.URL + "/download",
		ExecutablePath:   executable,
		GOOS:             "darwin",
		GOARCH:           "arm64",
		HTTPClient:       server.Client(),
	}
}

func newReleaseServer(t *testing.T, tag string, archive []byte, checksums string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(releaseHandler(t, tag, archive, checksums))
}

func releaseHandler(t *testing.T, tag string, archive []byte, checksums string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest":
			_, _ = fmt.Fprintf(w, `{"tag_name":%q}`, tag)
		case "/download/" + tag + "/grin_Darwin_arm64.tar.gz":
			_, _ = w.Write(archive)
		case "/download/" + tag + "/checksums.txt":
			_, _ = w.Write([]byte(checksums))
		default:
			http.NotFound(w, r)
		}
	}
}

func makeReleaseArchive(t *testing.T, name string, binary []byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(binary)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write(binary); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func writeFakeExecutable(t *testing.T, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "grin")
	if err := os.WriteFile(path, content, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
