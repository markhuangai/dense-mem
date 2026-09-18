package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/markhuangai/dense-mem/cmd/internal/serverapp"
)

const migrationControlRetirementCommand = "migration-control-retirement"

func main() {
	processCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var err error
	switch {
	case len(os.Args) == 1:
		err = serverapp.RunFromEnvironment(processCtx, serverapp.RuntimeOptions{})
	case len(os.Args) == 2 && os.Args[1] == migrationControlRetirementCommand:
		err = serverapp.RunMigrationControlRetirement(processCtx)
	default:
		err = fmt.Errorf("usage: %s [%s]", os.Args[0], migrationControlRetirementCommand)
	}
	if err != nil {
		log.Fatalf("server runtime failed: %v", err)
	}
}
