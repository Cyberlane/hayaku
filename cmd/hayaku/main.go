package main

import (
	"context"
	"github.com/Cyberlane/hayaku/internal/app"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	os.Exit(app.Run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}
