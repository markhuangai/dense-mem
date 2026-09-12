package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/markhuangai/dense-mem/cmd/internal/serverapp"
)

func main() {
	processCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := serverapp.RunFromEnvironment(processCtx, serverapp.RuntimeOptions{}); err != nil {
		log.Fatalf("server runtime failed: %v", err)
	}
}
