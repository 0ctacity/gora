package main

import (
	"fmt"
	"os"

	"gioui.org/app"

	"gora/internal/cli"
	"gora/internal/studio"
)

func main() {
	guiStarted := false
	exit := cli.Run(os.Args[1:], os.Stdout, os.Stderr, func(config cli.LaunchConfig) error {
		switch config.Mode {
		case cli.LaunchApp:
			if err := studio.StartApp(config.Root, config.Document, config.SocketPath); err != nil {
				return err
			}
			guiStarted = true
			return nil
		case cli.LaunchStudio:
			if err := studio.Start(config.Root, config.Document, config.SocketPath); err != nil {
				return err
			}
			guiStarted = true
			return nil
		case cli.LaunchHeadless:
			return studio.RunHeadless(config.Root, config.Document, config.SocketPath)
		default:
			return fmt.Errorf("unknown launch mode %q", config.Mode)
		}
	})
	if !guiStarted {
		os.Exit(exit)
	}
	app.Main()
	os.Exit(exit)
}
