package runtime

import (
	"time"
)

type RunCommandRequest struct {
	Executable      string
	Args            []string
	Cwd             string
	Timeout         time.Duration
	AllowOutside    bool
	SafeEnvironment bool
}
type RunCommandResult struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}
type ListProcessesRequest struct{}
type ListProcessesResult struct {
	Processes []Process `json:"processes"`
}
type ProcessInfoRequest struct{ PID int }
type ProcessInfoResult struct {
	Process Process `json:"process"`
}
type Process struct {
	PID     int    `json:"pid"`
	Name    string `json:"name"`
	User    string `json:"user,omitempty"`
	Command string `json:"command"`
}
type SystemInfoResult struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
}
