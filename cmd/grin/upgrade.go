package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	latestReleaseURL       = "https://api.github.com/repos/duongnvt2110/grin/releases/latest"
	releaseDownloadBaseURL = "https://github.com/duongnvt2110/grin/releases/download"
	maxReleaseMetadata     = 1 << 20
	maxReleaseArchive      = 128 << 20
)

type upgradeOptions struct {
	LatestReleaseURL string
	DownloadBaseURL  string
	ExecutablePath   string
	GOOS             string
	GOARCH           string
	HTTPClient       *http.Client
}

type latestRelease struct {
	TagName string `json:"tag_name"`
}

func runUpgradeCLI(output io.Writer, currentVersion string, args []string) error {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		_, _ = io.WriteString(output, "Usage: grin upgrade\n\nUpgrade an installed Grin release to the latest stable GitHub release.\n")
		return nil
	}
	if len(args) != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(args, " "))
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate current executable: %w", err)
	}
	return runUpgrade(output, currentVersion, upgradeOptions{
		LatestReleaseURL: latestReleaseURL,
		DownloadBaseURL:  releaseDownloadBaseURL,
		ExecutablePath:   executable,
		GOOS:             runtime.GOOS,
		GOARCH:           runtime.GOARCH,
		HTTPClient:       &http.Client{Timeout: 30 * time.Second},
	})
}

func runUpgrade(output io.Writer, currentVersion string, options upgradeOptions) error {
	current, err := parseStableVersion(currentVersion)
	if err != nil {
		return fmt.Errorf("development builds cannot self-upgrade")
	}
	archiveName, err := releaseArchiveName(options.GOOS, options.GOARCH)
	if err != nil {
		return err
	}
	if options.HTTPClient == nil {
		return fmt.Errorf("HTTP client is required")
	}

	metadata, err := downloadUpgradeFile(options.HTTPClient, options.LatestReleaseURL, maxReleaseMetadata)
	if err != nil {
		return fmt.Errorf("get latest release: %w", err)
	}
	var release latestRelease
	if err := json.Unmarshal(metadata, &release); err != nil {
		return fmt.Errorf("decode latest release: %w", err)
	}
	latest, err := parseStableVersion(release.TagName)
	if err != nil {
		return fmt.Errorf("latest release has invalid version %q", release.TagName)
	}

	switch compareStableVersions(latest, current) {
	case 0:
		_, _ = fmt.Fprintf(output, "Grin is already up to date (%s)\n", normalizedVersion(currentVersion))
		return nil
	case -1:
		return fmt.Errorf("installed version %s is newer than latest release %s; refusing downgrade", normalizedVersion(currentVersion), normalizedVersion(release.TagName))
	}

	base := strings.TrimRight(options.DownloadBaseURL, "/") + "/" + release.TagName
	archive, err := downloadUpgradeFile(options.HTTPClient, base+"/"+archiveName, maxReleaseArchive)
	if err != nil {
		return fmt.Errorf("download %s: %w", archiveName, err)
	}
	checksums, err := downloadUpgradeFile(options.HTTPClient, base+"/checksums.txt", maxReleaseMetadata)
	if err != nil {
		return fmt.Errorf("download checksums: %w", err)
	}
	if err := verifyReleaseChecksum(archiveName, archive, checksums); err != nil {
		return err
	}
	binary, err := extractReleaseBinary(archive)
	if err != nil {
		return err
	}
	if err := replaceExecutable(options.ExecutablePath, binary); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(output, "Upgraded Grin %s → %s\n", normalizedVersion(currentVersion), normalizedVersion(release.TagName))
	return nil
}

func parseStableVersion(raw string) ([3]int, error) {
	var version [3]int
	normalized := normalizedVersion(raw)
	parts := strings.Split(normalized, ".")
	if len(parts) != 3 {
		return version, fmt.Errorf("invalid stable version")
	}
	for index, part := range parts {
		if part == "" {
			return version, fmt.Errorf("invalid stable version")
		}
		value, err := strconv.Atoi(part)
		if err != nil || value < 0 {
			return version, fmt.Errorf("invalid stable version")
		}
		version[index] = value
	}
	return version, nil
}

func normalizedVersion(raw string) string {
	return strings.TrimPrefix(strings.TrimSpace(raw), "v")
}

func compareStableVersions(left, right [3]int) int {
	for index := range left {
		if left[index] < right[index] {
			return -1
		}
		if left[index] > right[index] {
			return 1
		}
	}
	return 0
}

func releaseArchiveName(goos, goarch string) (string, error) {
	var osName string
	switch goos {
	case "darwin":
		osName = "Darwin"
	case "linux":
		osName = "Linux"
	default:
		return "", fmt.Errorf("unsupported operating system: %s", goos)
	}
	if goarch != "amd64" && goarch != "arm64" {
		return "", fmt.Errorf("unsupported architecture: %s", goarch)
	}
	return fmt.Sprintf("grin_%s_%s.tar.gz", osName, goarch), nil
}

func downloadUpgradeFile(client *http.Client, url string, maxBytes int64) ([]byte, error) {
	request, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "grin-upgrade")
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %s", response.Status)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("response exceeds size limit")
	}
	return data, nil
}

func verifyReleaseChecksum(archiveName string, archive, checksumFile []byte) error {
	var expected string
	for _, line := range strings.Split(string(checksumFile), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == archiveName {
			expected = fields[0]
			break
		}
	}
	if expected == "" {
		return fmt.Errorf("checksum entry for %s was not found", archiveName)
	}
	decoded, err := hex.DecodeString(expected)
	if err != nil || len(decoded) != sha256.Size {
		return fmt.Errorf("checksum entry for %s is invalid", archiveName)
	}
	actual := sha256.Sum256(archive)
	if !bytes.Equal(decoded, actual[:]) {
		return fmt.Errorf("checksum verification failed for %s", archiveName)
	}
	return nil
}

func extractReleaseBinary(archive []byte) ([]byte, error) {
	gzipReader, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("open release archive: %w", err)
	}
	defer gzipReader.Close()
	tarReader := tar.NewReader(gzipReader)
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read release archive: %w", err)
		}
		if filepath.Base(header.Name) != "grin" || header.Typeflag != tar.TypeReg {
			continue
		}
		binary, err := io.ReadAll(io.LimitReader(tarReader, maxReleaseArchive+1))
		if err != nil {
			return nil, fmt.Errorf("extract grin binary: %w", err)
		}
		if len(binary) == 0 || int64(len(binary)) > maxReleaseArchive {
			return nil, fmt.Errorf("release archive contains an invalid grin binary")
		}
		return binary, nil
	}
	return nil, fmt.Errorf("release archive did not contain the grin binary")
}

func replaceExecutable(executablePath string, binary []byte) error {
	resolved, err := filepath.EvalSymlinks(executablePath)
	if err != nil {
		return fmt.Errorf("resolve current executable: %w", err)
	}
	directory := filepath.Dir(resolved)
	temporary, err := os.CreateTemp(directory, ".grin-upgrade-*")
	if err != nil {
		return fmt.Errorf("install directory is not writable: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(binary); err != nil {
		temporary.Close()
		return fmt.Errorf("write replacement executable: %w", err)
	}
	if err := temporary.Chmod(0o755); err != nil {
		temporary.Close()
		return fmt.Errorf("set replacement executable permissions: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync replacement executable: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close replacement executable: %w", err)
	}
	if err := os.Rename(temporaryPath, resolved); err != nil {
		return fmt.Errorf("replace current executable: %w", err)
	}
	return nil
}
