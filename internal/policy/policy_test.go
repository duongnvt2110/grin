package policy

import (
	"context"
	"testing"
)

func TestNormalPolicyAppliesWorkspaceRules(t *testing.T) {
	tests := []struct {
		name        string
		tool        string
		outside     bool
		destructive bool
		decision    Decision
		reason      string
	}{
		{name: "read", tool: "process.list", decision: Allow},
		{name: "workspace write", tool: "fs.write_text", decision: Allow},
		{name: "workspace edit", tool: "fs.edit_text", decision: Allow},
		{name: "outside read", tool: "fs.read_text", outside: true, decision: Allow},
		{name: "outside write", tool: "fs.write_text", outside: true, decision: Ask, reason: "write outside workspace"},
		{name: "shell", tool: "shell.run", decision: Allow},
		{name: "destructive shell", tool: "shell.run", destructive: true, decision: Ask, reason: "destructive command"},
		{name: "outside cwd", tool: "shell.run", outside: true, decision: Ask, reason: "cwd outside workspace"},
		{name: "unknown tool", tool: "unknown.tool", decision: Deny},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := New().Evaluate(context.Background(), Operation{Tool: test.tool, Outside: test.outside, Destructive: test.destructive})
			if result.Decision != test.decision {
				t.Fatalf("decision = %s, want %s (%s)", result.Decision, test.decision, result.Reason)
			}
			if test.reason != "" && result.Reason != test.reason {
				t.Fatalf("reason = %q, want %q", result.Reason, test.reason)
			}
		})
	}
}

func TestYoloAllowsSupportedOperationsButNotUnknownTools(t *testing.T) {
	for _, tool := range []string{"fs.write_text", "fs.edit_text", "git.status", "shell.run", "fs.read_text"} {
		if result := New(true).Evaluate(context.Background(), Operation{Tool: tool}); result.Decision != Allow {
			t.Fatalf("YOLO %s decision = %s, want %s", tool, result.Decision, Allow)
		}
	}
	if result := New(true).Evaluate(context.Background(), Operation{Tool: "unknown.tool"}); result.Decision != Deny {
		t.Fatalf("YOLO unknown decision = %s, want %s", result.Decision, Deny)
	}
}
