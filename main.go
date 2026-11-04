package main

import (
	"context"
	"fmt"
	"github.com/doomerlabs/doomer/cmd"
	"os"
	"os/signal"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	handled, err := cmd.DispatchCLI(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	if !handled && err == nil {
		err = cmd.NewRootCommand().ExecuteContext(ctx)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
