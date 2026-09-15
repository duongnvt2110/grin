package app

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"

	"grin/internal/approval"
	"grin/internal/events"
	"grin/internal/tui"
)

func ContextWithShutdown(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithCancel(parent)
}

func (a *Application) Run(ctx context.Context) error {
	subscription := a.bus.Subscribe()
	reliableSubscription := a.bus.SubscribeReliable()
	workspaceLabel := a.Config.Workspace.Root
	modeLabel := "normal"
	if !a.Config.Yolo {
		workspaceLabel = "multi-workspace"
	}
	if a.Config.Yolo {
		modeLabel = "yolo"
	}
	model := tui.NewModel(workspaceLabel, modeLabel, a.readiness, a.Config.Yolo)
	model.MaxEntries = a.Config.TUI.MaxEvents
	model.Attach(subscription.Events)
	model.AttachReliable(reliableSubscription.Events)
	model.SetActionSink(func(action tui.Action) {
		if action.ApprovalRequestID == "" || action.OperationDigest == "" {
			return
		}
		switch action.Type {
		case tui.ActionApprove, tui.ActionReject:
			outcome := events.ApprovalRejected
			if action.Type == tui.ActionApprove {
				outcome = events.ApprovalAllowed
			}
			_ = a.approval.Resolve(approval.ApprovalDecision{ApprovalRequestID: action.ApprovalRequestID, OperationDigest: action.OperationDigest, Outcome: outcome})
		}
	})
	program, tuiDone := tui.Start(ctx, model, a.input, a.output)
	defer subscription.Close()
	defer reliableSubscription.Close()
	stopTUI := func() {
		program.Quit()
		select {
		case <-tuiDone:
		case <-time.After(2 * time.Second):
		}
	}
	if err := a.readiness.Wait(ctx); err != nil {
		stopTUI()
		return err
	}
	listener, err := net.Listen("tcp", a.server.Addr)
	if err != nil {
		stopTUI()
		return err
	}
	a.ready.Store(true)
	serveErr := make(chan error, 1)
	go func() { serveErr <- a.server.Serve(listener) }()
	select {
	case <-ctx.Done():
		a.ready.Store(false)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		shutdownErr := a.server.Shutdown(shutdownCtx)
		cancel()
		stopTUI()
		if shutdownErr != nil {
			return shutdownErr
		}
		return nil
	case err := <-serveErr:
		a.ready.Store(false)
		stopTUI()
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case err := <-tuiDone:
		a.ready.Store(false)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		shutdownErr := a.server.Shutdown(shutdownCtx)
		cancel()
		if shutdownErr != nil {
			return shutdownErr
		}
		if err == nil {
			return nil
		}
		return err
	}
}
