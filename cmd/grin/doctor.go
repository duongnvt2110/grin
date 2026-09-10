package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"grin/internal/config"
)

func runDoctor(output io.Writer, args []string) error {
	checkReady := false
	configArgs := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == "--yolo" || arg == "-yolo" || strings.HasPrefix(arg, "--yolo=") || strings.HasPrefix(arg, "-yolo=") {
			return fmt.Errorf("doctor: --yolo is only supported by the Grin runtime command")
		}
		if arg == "--check-ready" {
			checkReady = true
			continue
		}
		if arg == "--help" {
			_, _ = io.WriteString(output, "Usage: grin doctor [--workspace PATH] [--port PORT] [--check-ready]\n")
			return nil
		}
		configArgs = append(configArgs, arg)
	}
	options, err := config.Parse(configArgs)
	if err != nil {
		return err
	}
	if !isLoopback(options.Config.Server.Bind) {
		return fmt.Errorf("doctor: Grin bind is not loopback")
	}

	endpoint := "http://" + options.Config.Server.Address() + "/mcp"
	if configured := os.Getenv("MCP_SERVER_URL"); configured != "" {
		if err := validateMCPURL(configured); err != nil {
			return fmt.Errorf("doctor: MCP_SERVER_URL is invalid: %w", err)
		}
		endpoint = configured
	}
	if err := validateMCPURL(endpoint); err != nil {
		return fmt.Errorf("doctor: local MCP endpoint is invalid: %w", err)
	}

	_, _ = fmt.Fprintln(output, "Grin doctor")
	_, _ = fmt.Fprintln(output, "loopback_bind=PASS")
	_, _ = fmt.Fprintln(output, "mcp_endpoint=loopback-only")
	_, _ = fmt.Fprintln(output, "tunnel_protocol=external official tunnel-client")
	for _, name := range []string{"CONTROL_PLANE_API_KEY", "CONTROL_PLANE_TUNNEL_ID", "MCP_SERVER_URL"} {
		state := "missing"
		if _, ok := os.LookupEnv(name); ok {
			state = "present"
		}
		_, _ = fmt.Fprintf(output, "%s=%s\n", name, state)
	}
	_, _ = fmt.Fprintln(output, "secret_values=never printed")
	_, _ = fmt.Fprintln(output, "public_inbound_binding=disabled")

	if checkReady {
		if err := checkReadiness(endpoint); err != nil {
			return fmt.Errorf("doctor: readiness check failed: %w", err)
		}
		_, _ = fmt.Fprintln(output, "readiness=PASS")
	}
	return nil
}

func checkReadiness(endpoint string) error {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return err
	}
	parsed.Path = "/readyz"
	parsed.RawQuery = ""
	parsed.Fragment = ""
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, parsed.String(), nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %s", response.Status)
	}
	return nil
}

func validateMCPURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if parsed.Scheme != "http" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("must be an HTTP URL without credentials, query, or fragment")
	}
	if parsed.Path != "/mcp" {
		return fmt.Errorf("path must be /mcp")
	}
	if !isLoopback(parsed.Hostname()) {
		return fmt.Errorf("host must be loopback")
	}
	if parsed.Port() == "" {
		return fmt.Errorf("port is required")
	}
	return nil
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
