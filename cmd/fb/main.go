// Command fb manages AVM FRITZ!Box routers from the terminal.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/mkrajinovic/fbctl/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Execute(ctx)
	stop()
	os.Exit(code)
}
