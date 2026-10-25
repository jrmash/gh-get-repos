package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/jrmash/gh-get-repos/internal/cli"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := cli.Execute(ctx, os.Args[1:], cli.Options{Version: version})
	stop()
	os.Exit(code)
}
