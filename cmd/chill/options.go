package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
)

var errUsage = errors.New("invalid command usage")

type readOptions struct {
	kubeconfig string
	context    string
	output     string
}

type inventoryOptions = readOptions
type systemOptions = readOptions

func parseInventoryOptions(args []string, stderr io.Writer) (inventoryOptions, error) {
	return parseReadOptions("inventory", args, stderr)
}

func parseSystemOptions(args []string, stderr io.Writer) (systemOptions, error) {
	return parseReadOptions("system", args, stderr)
}

func parseReadOptions(command string, args []string, stderr io.Writer) (readOptions, error) {
	opts := readOptions{}
	flags := flag.NewFlagSet("kubectl chill "+command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&opts.kubeconfig, "kubeconfig", "", "path to a kubeconfig file")
	flags.StringVar(&opts.context, "context", "", "kubeconfig context override")
	flags.StringVar(&opts.output, "o", "table", "output format: table or json")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return readOptions{}, flag.ErrHelp
		}
		return readOptions{}, fmt.Errorf("%w: %v", errUsage, err)
	}
	if flags.NArg() != 0 {
		return readOptions{}, fmt.Errorf("%w: %s accepts no positional arguments", errUsage, command)
	}
	if opts.output != "table" && opts.output != "json" {
		return readOptions{}, fmt.Errorf("%w: output must be table or json", errUsage)
	}
	return opts, nil
}
