// Command musafoz is a local media toolkit.
package main

import (
	"context"
	"os"
	"os/signal"

	// The fallback roots are used only when the system has no certificate
	// store, as on minimal Linux images, so managed downloads still verify.
	_ "golang.org/x/crypto/x509roots/fallback"

	"github.com/fabiotnt/musafoz/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := cli.Execute(ctx)
	stop()
	os.Exit(code)
}
