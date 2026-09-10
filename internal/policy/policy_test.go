package policy

import (
	"context"
	"testing"
)

func TestProfilesApplyV1CapabilityRules(t *testing.T) {
	tests := []struct {
		name        string
		profile     string
		tool        string
		outside     bool
		destructive bool
		decision    Decision
		reason      string
	}{
		{name: "workspace read", profile: "workspace", tool: "process.list", decision: Allow},
		{name: "workspace write", profile: "workspace", tool: "fs.write_text", decision: Allow},
		{name: "workspace edit", profile: "workspace", tool: "fs.edit_text", decision: Allow},
		{name: "workspace outside read", profile: "workspace", tool: "fs.read_text", outside: true, decision: Allow},
		{name: "workspace outside write", profile: "workspace", tool: "fs.write_text", outside: true, decision: Ask, reason: "write outside workspace"},
		{name: "workspace outside edit", profile: "workspace", tool: "fs.edit_text", outside: true, decision: Ask, reason: "write outside workspace"},
		{name: "workspace shell", profile: "workspace", tool: "shell.run", decision: Allow},
		{name: "workspace destructive shell", profile: "workspace", tool: "shell.run", destructive: true, decision: Ask, reason: "destructive command"},
		{name: "workspace outside cwd", profile: "workspace", tool: "shell.run", outside: true, decision: Ask, reason: "cwd outside workspace"},
		{name: "read-only read", profile: "read-only", tool: "system.info", decision: Allow},
		{name: "read-only write", profile: "read-only", tool: "fs.write_text", decision: Deny},
		{name: "read-only edit", profile: "read-only", tool: "fs.edit_text", decision: Deny},
		{name: "read-only shell", profile: "read-only", tool: "shell.run", decision: Deny},
		{name: "read-only outside read", profile: "read-only", tool: "fs.read_text", outside: true, decision: Deny},
		{name: "restricted read", profile: "restricted", tool: "process.info", decision: Ask},
		{name: "restricted git read", profile: "restricted", tool: "git.status", decision: Ask},
		{name: "restricted edit", profile: "restricted", tool: "fs.edit_text", decision: Ask},
		{name: "restricted shell", profile: "restricted", tool: "shell.run", decision: Ask},
		{name: "restricted outside read", profile: "restricted", tool: "fs.read_text", outside: true, decision: Deny},
		{name: "unknown tool", profile: "workspace", tool: "unknown.tool", decision: Deny},
		{name: "unknown profile", profile: "unknown", tool: "system.info", decision: Deny},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := New(test.profile).Evaluate(context.Background(), Operation{Tool: test.tool, Outside: test.outside, Destructive: test.destructive})
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
		if result := New("workspace", true).Evaluate(context.Background(), Operation{Tool: tool}); result.Decision != Allow {
			t.Fatalf("YOLO %s decision = %s, want %s", tool, result.Decision, Allow)
		}
	}
	if result := New("workspace", true).Evaluate(context.Background(), Operation{Tool: "unknown.tool"}); result.Decision != Deny {
		t.Fatalf("YOLO unknown decision = %s, want %s", result.Decision, Deny)
	}
	if result := New("unknown", true).Evaluate(context.Background(), Operation{Tool: "fs.read_text"}); result.Decision != Allow {
		t.Fatalf("YOLO unknown profile decision = %s, want %s", result.Decision, Allow)
	}
}
