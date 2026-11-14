package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/markhuangai/dense-mem/internal/evalretire"
)

func main() {
	command := evalretire.RuntimeCommand{}
	flag.StringVar(&command.Action, "action", "verify", "maintenance action: record, retire or verify")
	flag.StringVar(&command.Directory, "runtime-dir", "", "historical runtime ownership receipt directory")
	flag.StringVar(&command.Project, "compose-project", "", "exact project for explicit enrollment")
	flag.StringVar(&command.RetentionPlan, "retention-plan", "", "private retention manifest outside disposable storage")
	flag.DurationVar(&command.StopTimeout, "stop-timeout", time.Minute, "graceful stop observation bound; no forced removal")
	flag.Func("fixture-container", "exact terminal fixture carrying the recorded ownership label", func(id string) error {
		command.Fixtures = append(command.Fixtures, id)
		return nil
	})
	flag.Parse()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected positional arguments")
		os.Exit(2)
	}
	if err := evalretire.RunRuntimeCommand(ctx, command, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
