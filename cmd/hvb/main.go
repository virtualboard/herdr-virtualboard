// Command hvb is the herdr-virtualboard plugin: a kanban board over a
// VirtualBoard workspace, and the CLI its dispatched agents report through.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/virtualboard/herdr-virtualboard/internal/cli"
)

// version is set by the linker: -ldflags "-X main.version=0.2.0".
var version = "dev"

func main() {
	cli.Version = version

	app := &cli.App{Out: os.Stdout, Err: os.Stderr}
	root := cli.NewRoot(app)

	// Interrupt cancels the command's context so a dispatch in flight can
	// unwind — closing a half-created pane matters more than exiting fast.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := root.ExecuteContext(ctx); err != nil {
		app.ReportError(err)
		os.Exit(cli.ExitCode(err))
	}
}
