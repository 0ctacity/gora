package main

import (
	"os"

	"gioui.org/app"

	"gora/internal/cli"
	"gora/internal/studio"
)

func main() {
	started := false
	exit := cli.Run(os.Args[1:], os.Stdout, os.Stderr, func(config cli.LaunchConfig) error {
		if err := studio.Start(config.Root, config.Document, config.SocketPath); err != nil {
			return err
		}
		started = true
		return nil
	})
	if !started {
		os.Exit(exit)
	}
	app.Main()
	os.Exit(exit)
}
