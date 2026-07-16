package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	options := options{}
	options.bindFlags(flag.CommandLine)
	flag.Parse()
	if err := options.validate(); err != nil {
		log.Printf("invalid PowerObserver options: %v", err)
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := execute(ctx, options, &http.Client{}, options.startWaitTimeout, signalPollInterval); err != nil {
		log.Printf("PowerObserver failed: %v", err)
		os.Exit(4)
	}
}
