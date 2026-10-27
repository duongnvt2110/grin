package redaction

import (
	"strings"
	"testing"
)

func TestSanitizeInlineSensitiveAssignments(t *testing.T) {
	const id = "01a14001-2537-76e0-92d6-91eba1234567"
	tests := []struct {
		name, input, want string
	}{
		{"inline token", "Review token=" + id, "Review token=<redacted>"},
		{"inline spaced token", "Review token= " + id, "Review token= <redacted>"},
		{"inline quoted token", "Review token=\"" + id + "\"", "Review token=\"<redacted>\""},
		{"inline spaced quoted", "Review token= \"" + id + "\"", "Review token= \"<redacted>\""},
		{"inline colon", "Review secret: " + id, "Review secret: <redacted>"},
		{"separate field", "Review token=secret | " + id, "Review token=<redacted> | " + id},
		{"unrelated assignment", "Review ticket=note | " + id, "Review ticket=note | " + id},
		{"json secret", "{\"token\":\"" + id + "\"}", "{\"token\":\"<redacted>\"}"},
		{"url credentials", "https://user:pass@host.example/path", "https://<redacted>@host.example/path"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeText(tt.input)
			if got != tt.want {
				t.Fatalf("SanitizeText() = %q, want %q", got, tt.want)
			}
			if strings.Contains(tt.name, "token") || tt.name == "inline colon" || tt.name == "json secret" {
				if strings.Contains(got, "91eba1234567") {
					t.Fatalf("sensitive UUID leaked: %q", got)
				}
			}
		})
	}
}
