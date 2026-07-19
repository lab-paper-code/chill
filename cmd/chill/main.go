package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := execute(ctx, os.Args[1:], os.Stdout, os.Stderr, productionDependencies()); err != nil {
		fmt.Fprintf(os.Stderr, "kubectl chill: %v\n", err)
		if errors.Is(err, errUsage) {
			os.Exit(2)
		}
		os.Exit(1)
	}
}
