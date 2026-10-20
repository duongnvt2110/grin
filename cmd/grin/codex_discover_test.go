package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestCodexDiscoverHelpAndArgumentValidation(t *testing.T) {
	var output bytes.Buffer
	if err := runCodexDiscover(&output, []string{"--help"}); err != nil || !strings.Contains(output.String(), "grin codex discover") {
		t.Fatalf("help = %q, err %v", output.String(), err)
	}
	if err := runCodexDiscover(&output, []string{"unexpected"}); err == nil {
		t.Fatal("unexpected argument accepted")
	}
}
