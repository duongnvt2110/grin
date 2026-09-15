package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"grin/internal/app"
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
