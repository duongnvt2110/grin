package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

func readTerminalTitles(ctx context.Context, goos string) ([]TerminalObservation, []string, bool, bool, string, error) {
	if goos == "darwin" {
		return readDarwinTerminalTitles(ctx)
	}
	runCtx, cancel := context.WithTimeout(ctx, terminalReadTimeout)
	defer cancel()
	return readTerminalScript(runCtx, goos)
}

func readDarwinTerminalTitles(ctx context.Context) ([]TerminalObservation, []string, bool, bool, string, error) {
	runCtx, cancel := context.WithTimeout(ctx, darwinReadTimeout)
	defer cancel()
	titleCtx, cancelTitle := context.WithTimeout(runCtx, darwinTitleTimeout)
	observations, sources, nativeSupported, nativeComplete, reason, titleErr := readTerminalScript(titleCtx, "darwin")
	cancelTitle()
	tmuxObservations, tmuxApplicable, tmuxComplete, tmuxErr := readScopedTmuxTitles(runCtx)
	nativeApplicable := titleErr != nil || len(sources) > 0
	if tmuxApplicable {
		sources = append(sources, "tmux-current-session")
	}
	if tmuxErr == nil && tmuxApplicable {
		observations = append(observations, tmuxObservations...)
	}
	supported := nativeSupported || tmuxErr == nil && tmuxApplicable
	complete := (nativeApplicable && nativeSupported && nativeComplete || !nativeApplicable) &&
		(tmuxApplicable && tmuxErr == nil && tmuxComplete || !tmuxApplicable) &&
		(nativeApplicable || tmuxApplicable)
	if tmuxErr != nil && reason == "" {
		reason = "scoped tmux title probe failed"
	}
	if titleErr != nil && reason == "" {
		reason = "native terminal title API could not be read"
	}
	return observations, sources, supported, complete, reason, nil
}

func readTerminalScript(ctx context.Context, goos string) ([]TerminalObservation, []string, bool, bool, string, error) {
	var name, input string
	switch goos {
	case "darwin":
		name, input = "darwin.js", "osascript"
	case "linux":
		name, input = "linux.sh", "sh"
	case "windows":
		name, input = "windows.ps1", "powershell.exe"
	default:
		return nil, nil, false, false, "terminal title discovery is unsupported on this OS", nil
	}
	if _, err := exec.LookPath(input); err != nil {
		return nil, nil, false, false, "native terminal title command is unavailable", nil
	}
	script, err := terminalDiscoveryScripts.ReadFile("terminal_discovery/" + name)
	if err != nil {
		return nil, nil, false, false, "bundled terminal title script is unavailable", err
	}
	var command *exec.Cmd
	switch goos {
	case "darwin":
		command = exec.CommandContext(ctx, input, "-l", "JavaScript", "-")
	case "linux":
		command = exec.CommandContext(ctx, input, "-s")
	case "windows":
		command = exec.CommandContext(ctx, input, "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", "-")
	}
	command.Stdin = bytes.NewReader(script)
	command.Stderr = io.Discard
	configureDiscoveryProcess(command)
	command.Cancel = func() error { return killDiscoveryProcess(command) }
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, nil, false, false, "native terminal title API could not be read", err
	}
	if err := command.Start(); err != nil {
		return nil, nil, false, false, "native terminal title API is unavailable", nil
	}
	output, readErr := io.ReadAll(io.LimitReader(stdout, maxDiscoveryOutput+1))
	if len(output) > maxDiscoveryOutput {
		_ = killDiscoveryProcess(command)
		_ = command.Wait()
		return nil, nil, false, false, "native terminal title output exceeded its limit", nil
	}
	waitErr := command.Wait()
	if readErr != nil {
		return nil, nil, false, false, "native terminal title output could not be read", readErr
	}
	if waitErr != nil {
		if ctx.Err() != nil {
			return nil, nil, false, false, "native terminal title probe timed out", context.DeadlineExceeded
		}
		return nil, nil, false, false, "native terminal title probe failed", waitErr
	}
	return parseTerminalOutput(goos, output)
}

func readScopedTmuxTitles(ctx context.Context) ([]TerminalObservation, bool, bool, error) {
	tmuxEnv, paneEnv := os.Getenv("TMUX"), os.Getenv("TMUX_PANE")
	socket, pane, ok := scopedTmuxTarget(tmuxEnv, paneEnv)
	if !ok {
		return nil, false, true, nil
	}
	tmux, err := exec.LookPath("tmux")
	if err != nil {
		return nil, true, false, err
	}
	probeCtx, cancel := context.WithTimeout(ctx, tmuxProbeTimeout)
	defer cancel()
	sessionOutput, err := runBoundedDiscoveryCommand(probeCtx, tmux, "-S", socket, "display-message", "-p", "-t", pane, "#{session_id}")
	if err != nil {
		if probeCtx.Err() != nil {
			return nil, true, false, context.DeadlineExceeded
		}
		return nil, true, false, err
	}
	session := strings.TrimSpace(string(sessionOutput))
	if len(session) < 2 || session[0] != '$' || strings.Trim(session[1:], "0123456789") != "" {
		return nil, true, false, errors.New("tmux returned an invalid session identifier")
	}
	format := "#{pane_id}\037#{pane_title}\037#{pane_current_path}\037#{pane_active}"
	output, err := runBoundedDiscoveryCommand(probeCtx, tmux, "-S", socket, "list-panes", "-s", "-t", session, "-F", format)
	if err != nil {
		if probeCtx.Err() != nil {
			return nil, true, false, context.DeadlineExceeded
		}
		return nil, true, false, err
	}
	observations := make([]TerminalObservation, 0)
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	complete := len(lines) <= maxDiscoverySurfaces
	if len(lines) > maxDiscoverySurfaces {
		lines = lines[:maxDiscoverySurfaces]
	}
	for _, line := range lines {
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\x1f", 4)
		if len(fields) != 4 || !validTmuxPaneID(fields[0]) {
			complete = false
			continue
		}
		if !hasPotentialUUID(fields[1]) {
			continue
		}
		if len(observations) == maxTerminalWindows {
			complete = false
			break
		}
		focused := fields[0] == pane && fields[3] == "1"
		observations = append(observations, TerminalObservation{
			API: "tmux", ID: session + ":" + fields[0], Title: fields[1], Workspace: fields[2],
			Foreground: focused, Focused: focused,
		})
	}
	return observations, true, complete, nil
}

func scopedTmuxTarget(tmuxEnv, paneEnv string) (socket, pane string, ok bool) {
	socket, _, found := strings.Cut(tmuxEnv, ",")
	if !found || !filepath.IsAbs(socket) || !validTmuxPaneID(paneEnv) {
		return "", "", false
	}
	return socket, paneEnv, true
}

func validTmuxPaneID(value string) bool {
	if len(value) < 2 || value[0] != '%' {
		return false
	}
	for _, digit := range value[1:] {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}

func runBoundedDiscoveryCommand(ctx context.Context, executable string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, executable, args...)
	command.Stderr = io.Discard
	configureDiscoveryProcess(command)
	command.Cancel = func() error { return killDiscoveryProcess(command) }
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := command.Start(); err != nil {
		return nil, err
	}
	output, readErr := io.ReadAll(io.LimitReader(stdout, maxDiscoveryOutput+1))
	if len(output) > maxDiscoveryOutput {
		_ = killDiscoveryProcess(command)
		_ = command.Wait()
		return nil, errors.New("discovery command output exceeded its limit")
	}
	waitErr := command.Wait()
	if readErr != nil {
		return nil, readErr
	}
	if waitErr != nil {
		return nil, waitErr
	}
	return output, nil
}

func parseTerminalOutput(goos string, output []byte) ([]TerminalObservation, []string, bool, bool, string, error) {
	if goos == "linux" {
		return parseLinuxTerminalOutput(string(output))
	}
	var snapshot terminalSnapshot
	if err := json.Unmarshal(output, &snapshot); err != nil {
		return nil, nil, false, false, "native terminal title output was malformed", err
	}
	if !snapshot.Supported {
		return snapshot.Terminals, snapshot.Sources, false, false, snapshot.Reason, nil
	}
	if len(snapshot.Terminals) > maxTerminalWindows {
		snapshot.Terminals = snapshot.Terminals[:maxTerminalWindows]
		snapshot.Complete = false
	}
	return snapshot.Terminals, snapshot.Sources, true, snapshot.Complete, snapshot.Reason, nil
}

func parseLinuxTerminalOutput(output string) ([]TerminalObservation, []string, bool, bool, string, error) {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	supported := false
	complete := false
	completeSeen := false
	malformedX11 := false
	reason := "Linux terminal title APIs are unavailable"
	sources := []string{}
	terminals := make([]TerminalObservation, 0, len(lines))
	for _, line := range lines {
		switch {
		case line == "SUPPORTED":
			supported = true
			continue
		case strings.HasPrefix(line, "UNSUPPORTED\t"):
			reason = strings.TrimPrefix(line, "UNSUPPORTED\t")
			continue
		case strings.HasPrefix(line, "SOURCE\t"):
			sources = append(sources, strings.TrimPrefix(line, "SOURCE\t"))
			continue
		case strings.HasPrefix(line, "SOURCES\t"):
			sources = append(sources, strings.Split(strings.TrimPrefix(line, "SOURCES\t"), "\t")...)
			continue
		case line == "SOURCES":
			continue
		case strings.HasPrefix(line, "COMPLETE\t"):
			completeSeen = true
			complete = strings.TrimPrefix(line, "COMPLETE\t") == "true"
			continue
		case line == "":
			continue
		}
		fields := strings.SplitN(line, "\t", 6)
		if len(fields) != 6 {
			return nil, sources, false, false, "Linux terminal title API returned malformed data", fmt.Errorf("invalid Linux terminal record")
		}
		title := fields[5]
		if fields[0] == "x11-ewmh" {
			if index := strings.Index(title, " = "); index >= 0 {
				title = title[index+3:]
			}
			var err error
			title, err = strconv.Unquote(title)
			if err != nil {
				malformedX11 = true
				continue
			}
		}
		terminals = append(terminals, TerminalObservation{
			API:        fields[0],
			ID:         fields[1],
			Title:      title,
			Workspace:  fields[4],
			Foreground: fields[2] == "true",
			Focused:    fields[3] == "true",
		})
		if len(terminals) == maxTerminalWindows {
			break
		}
	}
	if !supported {
		if reason == "Linux terminal title APIs are unavailable" {
			return nil, sources, false, false, "Linux terminal title API returned an unsupported response", fmt.Errorf("invalid Linux terminal response")
		}
		return terminals, sources, false, false, reason, nil
	}
	// The script's COMPLETE marker can follow malformed records. Never let
	// that marker authorize a scan which discarded unreadable X11 titles.
	complete = completeSeen && complete && !malformedX11
	return terminals, sources, true, complete, "", nil
}
