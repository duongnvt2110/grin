package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDoctorReportsBoundaryWithoutSecretValues(t *testing.T) {
	t.Setenv("CONTROL_PLANE_API_KEY", "api-key-must-not-print")
	t.Setenv("CONTROL_PLANE_TUNNEL_ID", "tunnel-id-must-not-print")
	t.Setenv("MCP_SERVER_URL", "http://127.0.0.1:8765/mcp")
	var output bytes.Buffer
	if err := runDoctor(&output, []string{"--workspace", t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, expected := range []string{"loopback_bind=PASS", "mcp_endpoint=loopback-only", "CONTROL_PLANE_API_KEY=present", "CONTROL_PLANE_TUNNEL_ID=present", "secret_values=never printed", "public_inbound_binding=disabled"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("doctor output missing %q: %s", expected, text)
		}
	}
	for _, secret := range []string{"api-key-must-not-print", "tunnel-id-must-not-print"} {
		if strings.Contains(text, secret) {
			t.Fatalf("doctor leaked secret %q: %s", secret, text)
		}
	}
}

func TestDoctorRejectsPublicMCPURL(t *testing.T) {
	t.Setenv("MCP_SERVER_URL", "http://203.0.113.10:8765/mcp")
	if err := runDoctor(&bytes.Buffer{}, []string{"--workspace", t.TempDir()}); err == nil {
		t.Fatal("public MCP URL was accepted")
	}
}

func TestDoctorChecksReadinessWithoutPrintingEndpointCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/readyz" {
			t.Errorf("readiness path = %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	t.Setenv("MCP_SERVER_URL", server.URL+"/mcp")
	var output bytes.Buffer
	if err := runDoctor(&output, []string{"--workspace", t.TempDir(), "--check-ready"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "readiness=PASS") {
		t.Fatalf("doctor output = %q", output.String())
	}
}

func TestDoctorRejectsEndpointCredentials(t *testing.T) {
	t.Setenv("MCP_SERVER_URL", "http://user:password@127.0.0.1:8765/mcp")
	if err := runDoctor(&bytes.Buffer{}, []string{"--workspace", t.TempDir()}); err == nil {
		t.Fatal("endpoint credentials were accepted")
	}
}

func TestDoctorHelp(t *testing.T) {
	var output bytes.Buffer
	if err := runDoctor(&output, []string{"--help"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "grin doctor") {
		t.Fatalf("help output = %q", output.String())
	}
}

func TestDoctorRejectsYolo(t *testing.T) {
	for _, arg := range []string{"--yolo", "--yolo=true", "-yolo", "-yolo=true"} {
		t.Run(arg, func(t *testing.T) {
			if err := runDoctor(&bytes.Buffer{}, []string{arg}); err == nil {
				t.Fatalf("doctor accepted %s", arg)
			}
		})
	}
}
