package policy

import (
	"context"
	"strings"
)

type Decision string

const (
	Allow Decision = "ALLOW"
	Ask   Decision = "ASK"
	Deny  Decision = "DENY"
)

type Operation struct {
	Tool        string
	Capability  string
	Arguments   any
	Outside     bool
	Destructive bool
}

type Result struct {
	Decision Decision
	Reason   string
}

type Evaluator interface {
	Evaluate(context.Context, Operation) Result
}

type ProfileEvaluator struct {
	Profile string
	Yolo    bool
}

func New(profile string, yoloFlag ...bool) Evaluator {
	return ProfileEvaluator{Profile: profile, Yolo: len(yoloFlag) > 0 && yoloFlag[0]}
}

func (e ProfileEvaluator) Evaluate(_ context.Context, operation Operation) Result {
	profile := strings.ToLower(strings.TrimSpace(e.Profile))
	if profile == "" {
		profile = "workspace"
	}
	readOnly := isReadOnly(operation.Tool)
	sideEffect := isSideEffect(operation.Tool)
	if !readOnly && !sideEffect {
		return Result{Decision: Deny, Reason: "tool is not supported"}
	}
	if e.Yolo {
		return Result{Decision: Allow, Reason: "YOLO mode"}
	}
	switch profile {
	case "read-only":
		if operation.Outside {
			return Result{Decision: Deny, Reason: "outside workspace access is disabled by the read-only profile"}
		}
		if readOnly {
			return Result{Decision: Allow, Reason: "read-only operation"}
		}
		return Result{Decision: Deny, Reason: "operation is disabled by the read-only profile"}
	case "restricted":
		if operation.Outside {
			return Result{Decision: Deny, Reason: "outside workspace access is disabled by the restricted profile"}
		}
		if readOnly {
			return Result{Decision: Ask, Reason: "restricted profile requires local approval"}
		}
		if sideEffect {
			return Result{Decision: Ask, Reason: "side effects require local approval"}
		}
	case "workspace":
		if operation.Tool == "shell.run" && operation.Destructive {
			return Result{Decision: Ask, Reason: "destructive command"}
		}
		if operation.Tool == "shell.run" && operation.Outside {
			return Result{Decision: Ask, Reason: "cwd outside workspace"}
		}
		if isWrite(operation.Tool) && operation.Outside {
			return Result{Decision: Ask, Reason: "write outside workspace"}
		}
		if readOnly {
			return Result{Decision: Allow, Reason: "read-only operation"}
		}
		if isWrite(operation.Tool) {
			return Result{Decision: Allow, Reason: "workspace write"}
		}
		if operation.Tool == "shell.run" {
			return Result{Decision: Allow, Reason: "workspace shell"}
		}
	default:
		return Result{Decision: Deny, Reason: "unknown policy profile"}
	}
	return Result{Decision: Deny, Reason: "tool is not enabled by the active policy"}
}

func isReadOnly(tool string) bool {
	switch tool {
	case "fs.list", "fs.stat", "fs.read_text", "fs.search", "workspace.info", "process.list", "process.info", "system.info", "git.status", "git.diff", "git.log", "git.show":
		return true
	default:
		return false
	}
}

func isSideEffect(tool string) bool {
	return isWrite(tool) || tool == "shell.run"
}

func isWrite(tool string) bool {
	return tool == "fs.write_text" || tool == "fs.edit_text"
}
