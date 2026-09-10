package runtime

import (
	"context"
	"time"
)

type ListFilesRequest struct{ Path string }
type ListFilesResult struct{ Entries []string }
type StatRequest struct{ Path string }
type StatResult struct {
	Path  string
	Size  int64
	IsDir bool
}
type ReadTextRequest struct {
	Path     string
	MaxBytes int64
}
type ReadTextResult struct {
	Path, Content string
	Bytes         int64
}
type SearchRequest struct {
	Path, Query string
	MaxResults  int
}
type SearchResult struct{ Matches []string }
type WriteTextRequest struct {
	Path, Content string
	Overwrite     bool
}
type WriteTextResult struct {
	Path  string
	Bytes int64
}
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

type Runtime interface {
	ListFiles(context.Context, ListFilesRequest) (ListFilesResult, error)
	Stat(context.Context, StatRequest) (StatResult, error)
	ReadText(context.Context, ReadTextRequest) (ReadTextResult, error)
	Search(context.Context, SearchRequest) (SearchResult, error)
	WriteText(context.Context, WriteTextRequest) (WriteTextResult, error)
	RunCommand(context.Context, RunCommandRequest) (RunCommandResult, error)
	ListProcesses(context.Context, ListProcessesRequest) (ListProcessesResult, error)
	ProcessInfo(context.Context, ProcessInfoRequest) (ProcessInfoResult, error)
	SystemInfo(context.Context) (SystemInfoResult, error)
}
