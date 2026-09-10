package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestReleaseCLIUsesLoopbackAndRedactsTunnelValues(t *testing.T) {
	t.Setenv("CONTROL_PLANE_API_KEY", "release-test-key")
	t.Setenv("CONTROL_PLANE_TUNNEL_ID", "release-test-tunnel")
	t.Setenv("MCP_SERVER_URL", "http://127.0.0.1:8765/mcp")
	var output bytes.Buffer
	if err := runDoctor(&output, []string{"--workspace", t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, required := range []string{"loopback_bind=PASS", "mcp_endpoint=loopback-only", "secret_values=never printed"} {
		if !strings.Contains(text, required) {
			t.Fatalf("release doctor output missing %q: %s", required, text)
		}
	}
	for _, secret := range []string{"release-test-key", "release-test-tunnel"} {
		if strings.Contains(text, secret) {
			t.Fatalf("release doctor output leaked %q", secret)
		}
	}
}
