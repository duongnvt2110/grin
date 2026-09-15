package runtime

import (
	"regexp"
	"strconv"
	"strings"
)

var sensitiveNamePattern = regexp.MustCompile(`(?i)(token|password|passwd|secret|api[-_.]?key|private[-_.]?key|credential|auth)`)
var assignmentPattern = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)=(.*)$`)

// RedactCommand returns a safe display form for command approval and process
// metadata. It never changes the command that is executed.
func RedactCommand(command string, args []string) string {
	fields := make([]string, 1, len(args)+1)
	fields[0] = command
	fields = append(fields, args...)
	return strings.Join(redactProcessFields(fields), " ")
}

func parseProcesses(output string) []Process {
	lines := strings.Split(output, "\n")
	processes := make([]Process, 0, len(lines))
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil || pid <= 0 {
			continue
		}
		command := ""
		if len(fields) > 3 {
			command = strings.Join(fields[3:], " ")
		}
		if command == "" {
			command = fields[2]
		}
		processes = append(processes, Process{
			PID:     pid,
			User:    fields[1],
			Name:    redactProcessText(fields[2]),
			Command: redactProcessText(command),
		})
	}
	return processes
}

func redactProcessText(value string) string {
	return strings.Join(redactProcessFields(strings.Fields(value)), " ")
}

func redactProcessFields(fields []string) []string {
	redacted := make([]string, 0, len(fields))
	redactNext := false
	for _, field := range fields {
		if redactNext {
			redacted = append(redacted, "<redacted>")
			redactNext = false
			continue
		}
		if match := assignmentPattern.FindStringSubmatch(field); match != nil {
			redacted = append(redacted, match[1]+"=<redacted>")
			continue
		}
		if redactedURL, ok := redactURLCredentials(field); ok {
			redacted = append(redacted, redactedURL)
			continue
		}
		key, separator, _ := strings.Cut(field, "=")
		if separator != "" && sensitiveProcessName(key) {
			redacted = append(redacted, key+"=<redacted>")
			continue
		}
		if sensitiveProcessName(strings.TrimLeft(key, "-")) {
			redacted = append(redacted, field)
			redactNext = separator == ""
			continue
		}
		redacted = append(redacted, field)
	}
	return redacted
}

func redactURLCredentials(value string) (string, bool) {
	scheme := strings.Index(value, "://")
	if scheme < 0 {
		return "", false
	}
	userinfoStart := scheme + 3
	at := strings.IndexByte(value[userinfoStart:], '@')
	if at < 0 {
		return "", false
	}
	at += userinfoStart
	return value[:userinfoStart] + "<redacted>@" + value[at+1:], true
}

func sensitiveProcessName(value string) bool {
	return sensitiveNamePattern.MatchString(value)
}
