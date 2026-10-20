package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"grin/internal/app"
	"grin/internal/codex"
	"grin/internal/config"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "init" {
		root, help, err := config.Init(os.Args[2:])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		if help {
			fmt.Print(config.InitUsage())
			return
		}
		fmt.Println("registered workspace:", root)
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "doctor" {
		if err := runDoctor(os.Stdout, os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "upgrade" {
		if err := runUpgradeCLI(os.Stdout, version, os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "grin upgrade:", err)
			os.Exit(2)
		}
		return
	}
	if len(os.Args) > 2 && os.Args[1] == "codex" && os.Args[2] == "discover" {
		if err := runCodexDiscover(os.Stdout, os.Args[3:]); err != nil {
			fmt.Fprintln(os.Stderr, "grin codex discover:", err)
			os.Exit(2)
		}
		return
	}
	options, err := config.Parse(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if options.Help {
		fmt.Print(config.Usage())
		return
	}
	if options.Version {
		fmt.Println(version)
		return
	}

	application, err := app.New(options.Config, version)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := application.Run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runCodexDiscover(output io.Writer, args []string) error {
	workspace := ""
	for index := 0; index < len(args); index++ {
		switch {
		case args[index] == "--help" || args[index] == "-h":
			_, _ = io.WriteString(output, "Usage: grin codex discover [--workspace PATH]\n\nRead terminal titles and return fail-closed Codex thread candidates as JSON.\n")
			return nil
		case args[index] == "--workspace":
			if index+1 == len(args) {
				return fmt.Errorf("--workspace requires a path")
			}
			index++
			workspace = args[index]
		case strings.HasPrefix(args[index], "--workspace="):
			workspace = strings.TrimPrefix(args[index], "--workspace=")
		default:
			return fmt.Errorf("unexpected argument %q", args[index])
		}
	}
	if workspace == "" {
		var err error
		workspace, err = os.Getwd()
		if err != nil {
			return fmt.Errorf("get current directory: %w", err)
		}
	}
	if !filepath.IsAbs(workspace) {
		var err error
		workspace, err = filepath.Abs(workspace)
		if err != nil {
			return fmt.Errorf("resolve workspace: %w", err)
		}
	}
	result, err := codex.New().Discover(context.Background(), workspace)
	if err != nil {
		return err
	}
	return result.WriteJSON(output)
}
