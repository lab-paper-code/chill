package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
)

var errUsage = errors.New("invalid command usage")

type inventoryOptions struct {
	kubeconfig string
	context    string
	output     string
}

func parseInventoryOptions(args []string, stderr io.Writer) (inventoryOptions, error) {
	opts := inventoryOptions{}
	flags := flag.NewFlagSet("kubectl chill inventory", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&opts.kubeconfig, "kubeconfig", "", "path to a kubeconfig file")
	flags.StringVar(&opts.context, "context", "", "kubeconfig context override")
	flags.StringVar(&opts.output, "o", "table", "output format: table or json")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return inventoryOptions{}, flag.ErrHelp
		}
		return inventoryOptions{}, fmt.Errorf("%w: %v", errUsage, err)
	}
	if flags.NArg() != 0 {
		return inventoryOptions{}, fmt.Errorf("%w: inventory accepts no positional arguments", errUsage)
	}
	if opts.output != "table" && opts.output != "json" {
		return inventoryOptions{}, fmt.Errorf("%w: output must be table or json", errUsage)
	}
	return opts, nil
}
