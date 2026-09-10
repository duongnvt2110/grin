package main

import (
	"strings"
	"testing"

	"grin/internal/config"
)

func TestCLIIdentityAndInteractiveUsage(t *testing.T) {
	if strings.TrimSpace(version) == "" {
		t.Fatal("CLI version must be set")
	}
	usage := config.Usage()
	for _, expected := range []string{"grin [--workspace PATH]", "searches upward for the nearest .grin/config.yaml", "--yolo", "--version", "--headless is not supported"} {
		if !strings.Contains(usage, expected) {
			t.Fatalf("usage missing %q: %s", expected, usage)
		}
	}
}
