//go:build darwin || linux

package codex

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestDiscoveryTimeoutKillsScriptChildren(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "late-child-marker")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	command := exec.CommandContext(ctx, "sh", "-c", "(sleep 0.2; touch \"$1\") & wait", "sh", marker)
	configureDiscoveryProcess(command)
	command.Cancel = func() error { return killDiscoveryProcess(command) }
	if err := command.Run(); err == nil {
		t.Fatal("timed out command succeeded")
	}
	time.Sleep(300 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("script child survived timeout; marker stat error = %v", err)
	}
}
