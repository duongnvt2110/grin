package policy

import (
	"context"
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

type PolicyEvaluator struct {
	Yolo bool
}

func New(yoloFlag ...bool) Evaluator {
	return PolicyEvaluator{Yolo: len(yoloFlag) > 0 && yoloFlag[0]}
}

func (e PolicyEvaluator) Evaluate(_ context.Context, operation Operation) Result {
	readOnly := isReadOnly(operation.Tool)
	sideEffect := isSideEffect(operation.Tool)
	if !readOnly && !sideEffect {
		return Result{Decision: Deny, Reason: "tool is not supported"}
	}
	if e.Yolo {
		return Result{Decision: Allow, Reason: "YOLO mode"}
	}
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
