package mcp

import (
	"grin/internal/errs"

	_ "github.com/modelcontextprotocol/go-sdk/mcp"
)

type Request struct {
	ID        string `json:"id"`
	Tool      string `json:"tool"`
	Arguments any    `json:"arguments"`
}

type ErrorResult struct {
	Error errs.Error `json:"error"`
}

type TransportError struct {
	Status  int
	Message string
}

func (e TransportError) Error() string { return e.Message }
