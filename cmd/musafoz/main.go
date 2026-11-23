// Command musafoz is a local media toolkit.
package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/fabiotnt/musafoz/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := cli.Execute(ctx)
	stop()
	os.Exit(code)
}
