package app

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"grin/internal/config"
)

func TestHealthAndReadinessEndpointsDoNotExposeWorkspaceData(t *testing.T) {
	cfg := config.Defaults()
	cfg.Workspace.Root = t.TempDir()
	application, err := New(cfg, "test")
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	recorder := httptest.NewRecorder()
	application.server.Handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Body.String() != "ok\n" {
		t.Fatalf("health response = %d %q", recorder.Code, recorder.Body.String())
	}
	if body := recorder.Body.String(); body == cfg.Workspace.Root {
		t.Fatal("health response exposed workspace root")
	}

	request = httptest.NewRequest(http.MethodGet, "/readyz", nil)
	recorder = httptest.NewRecorder()
	application.server.Handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("unready status = %d", recorder.Code)
	}
	application.ready.Store(true)
	recorder = httptest.NewRecorder()
	application.server.Handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Body.String() != "ready\n" {
		t.Fatalf("ready response = %d %q", recorder.Code, recorder.Body.String())
	}
}

func TestRunGatesReadinessOnTUIAndShutsDown(t *testing.T) {
	cfg := config.Defaults()
	cfg.Workspace.Root = t.TempDir()
	application, err := New(cfg, "test")
	if err != nil {
		t.Fatal(err)
	}
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := probe.Addr().String()
	_ = probe.Close()
	application.server.Addr = address
	reader, writer := io.Pipe()
	application.input = reader
	application.output = &bytes.Buffer{}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- application.Run(ctx) }()
	defer cancel()
	defer writer.Close()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		response, requestErr := http.Get("http://" + address + "/readyz")
		if requestErr == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	response, err := http.Get("http://" + address + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("ready status = %d", response.StatusCode)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("application did not shut down")
	}
}
