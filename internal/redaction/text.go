package redaction

import (
	"regexp"
	"strings"
)

var assignmentPattern = regexp.MustCompile(`^(\s*(?:export\s+)?["']?([A-Za-z_][A-Za-z0-9_.-]*)["']?\s*)([:=])(\s*)(.*)$`)
var jsonSensitiveFieldPattern = regexp.MustCompile(`(?i)(["']?([A-Za-z_][A-Za-z0-9_.-]*)["']?\s*:\s*)("(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*')`)
var urlCredentialPattern = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://)([A-Za-z0-9._~%+-]+):([A-Za-z0-9._~%:+-]+)@([A-Za-z0-9][A-Za-z0-9.-]*|\[[0-9A-Fa-f:.]+\])`)
var privateKeyMarkerPattern = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY(?: BLOCK)?-----`)

var sensitiveNames = []string{
	"password", "passwd", "pwd", "secret", "token",
	"credential", "credentials", "creds", "authorization", "cookie",
}

var sensitiveCompoundSuffixes = []string{
	"apikey", "authkey", "accesskey", "accesskeyid", "secretkey", "secretaccesskey",
	"privatekey", "servicekey", "accountkey", "clientkey", "dbkey", "databasekey",
	"clientsecret", "consumersecret", "jwtsecret",
	"accesstoken", "refreshtoken", "authtoken", "apitoken", "jwttoken",
	"dbpassword", "databasepassword", "dbpasswd", "databasepasswd",
	"dbpwd", "databasepwd", "dbpass", "databasepass",
}

// ContainsPrivateKey identifies PEM private-key blocks that must not be returned.
func ContainsPrivateKey(value string) bool {
	return privateKeyMarkerPattern.MatchString(value)
}

// SanitizeText redacts credential-shaped values while preserving surrounding text.
func SanitizeText(content string) string {
	var result strings.Builder
	result.Grow(len(content))
	for _, line := range strings.SplitAfter(content, "\n") {
		body := line
		terminator := ""
		if strings.HasSuffix(body, "\n") {
			body = strings.TrimSuffix(body, "\n")
			terminator = "\n"
			if strings.HasSuffix(body, "\r") {
				body = strings.TrimSuffix(body, "\r")
				terminator = "\r\n"
			}
		}

		replacedAssignment := false
		if match := assignmentPattern.FindStringSubmatch(body); match != nil {
			shortDeclaration := match[3] == ":" && match[4] == "" && strings.HasPrefix(match[5], "=")
			if !shortDeclaration && sensitiveName(match[2]) {
				body = match[1] + match[3] + match[4] + redactValue(match[5])
				replacedAssignment = true
			}
		}
		if !replacedAssignment {
			body = redactJSONFields(body)
			body = urlCredentialPattern.ReplaceAllString(body, `${1}<redacted>@${4}`)
		}
		result.WriteString(body)
		result.WriteString(terminator)
	}
	return result.String()
}

func redactJSONFields(value string) string {
	return jsonSensitiveFieldPattern.ReplaceAllStringFunc(value, func(field string) string {
		match := jsonSensitiveFieldPattern.FindStringSubmatch(field)
		if len(match) != 4 || !sensitiveName(match[2]) {
			return field
		}
		return match[1] + redactValue(match[3])
	})
}

func sensitiveName(name string) bool {
	lower := strings.ToLower(name)
	for _, candidate := range sensitiveNames {
		if lower == candidate || strings.HasSuffix(lower, "_"+candidate) || strings.HasSuffix(lower, "-"+candidate) || strings.HasSuffix(lower, "."+candidate) {
			return true
		}
	}
	normalized := strings.NewReplacer("_", "", "-", "", ".", "").Replace(lower)
	for _, suffix := range sensitiveCompoundSuffixes {
		if strings.HasSuffix(normalized, suffix) {
			return true
		}
	}
	return false
}

func redactValue(raw string) string {
	trimmed := strings.TrimSpace(raw)
	comma := ""
	if strings.HasSuffix(trimmed, ",") {
		trimmed = strings.TrimSpace(strings.TrimSuffix(trimmed, ","))
		comma = ","
	}
	if len(trimmed) >= 2 && (trimmed[0] == '"' || trimmed[0] == '\'') && trimmed[len(trimmed)-1] == trimmed[0] {
		quote := string(trimmed[0])
		return quote + "<redacted>" + quote + comma
	}
	return "<redacted>"
}
