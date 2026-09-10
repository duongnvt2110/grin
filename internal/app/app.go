package app

import (
	"io"
	"net/http"
	"os"
	"sync/atomic"

	"grin/internal/approval"
	"grin/internal/config"
	"grin/internal/events"
	"grin/internal/filesystem"
	gringit "grin/internal/git"
	grinmcp "grin/internal/mcp"
	"grin/internal/policy"
	grinruntime "grin/internal/runtime"
	"grin/internal/tui"
)

type Application struct {
	Config    config.Config
	server    *http.Server
	bus       *events.Bus
	approval  *approval.Manager
	readiness *tui.Readiness
	ready     atomic.Bool
	input     io.Reader
	output    io.Writer
}

func New(cfg config.Config, version string) (*Application, error) {
	if err := config.Validate(cfg); err != nil {
		return nil, err
	}
	files, err := filesystem.New(cfg.Workspace.Root, cfg.Limits, cfg.Yolo)
	if err != nil {
		return nil, err
	}
	runtimeService, err := grinruntime.New(cfg, files.Root())
	if err != nil {
		return nil, err
	}
	gitService := gringit.New(runtimeService, files.Root())
	bus := events.New(cfg.Limits.EventBufferSize)
	approvalManager := approval.New(bus, cfg.Limits.ApprovalTimeout)
	readiness := tui.NewReadiness()
	mcpServer := grinmcp.NewServer(grinmcp.Dependencies{Filesystem: files, Runtime: runtimeService, Git: gitService, Config: cfg, Version: version, Events: bus, Approval: approvalManager, Policy: policy.New(cfg.Policy.Profile, cfg.Yolo)})
	application := &Application{Config: cfg, bus: bus, approval: approvalManager, readiness: readiness, input: os.Stdin, output: os.Stdout}
	mux := http.NewServeMux()
	mux.Handle("/mcp", grinmcp.Handler(mcpServer))
	mux.HandleFunc("/healthz", func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/readyz", func(writer http.ResponseWriter, _ *http.Request) {
		if !application.ready.Load() {
			writer.WriteHeader(http.StatusServiceUnavailable)
			_, _ = writer.Write([]byte("not ready\n"))
			return
		}
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("ready\n"))
	})
	application.server = &http.Server{Addr: cfg.Server.Address(), Handler: mux}
	return application, nil
}
