package main

import (
	"flag"
	"time"

	"github.com/markhuangai/dense-mem/internal/evalharness"
)

func registerRuntimeFlags() *evalharness.RuntimeCommand {
	cmd := &evalharness.RuntimeCommand{}
	flag.StringVar(&cmd.Action, "runtime-action", "verify", "runtime lifecycle action: start, record, retire or verify")
	flag.StringVar(&cmd.Directory, "runtime-dir", "", "private runtime root containing the resource receipt and disposable bind directories")
	flag.StringVar(&cmd.Project, "compose-project", "", "exact Docker Compose project for first start or explicit enrollment")
	flag.StringVar(&cmd.ComposeFile, "compose-file", "", "Compose file for startup (defaults to examples/docker-compose.evaluation.yml)")
	flag.StringVar(&cmd.RetentionPlan, "retention-plan", "", "retirement evidence and protected-runtime manifest")
	flag.StringVar(&cmd.Supersedes, "supersedes", "", "retired runtime directory whose verified receipt permits replacement")
	flag.BoolVar(&cmd.New, "new-runtime", false, "explicitly start an independent runtime")
	flag.DurationVar(&cmd.StopTimeout, "stop-timeout", time.Minute, "graceful shutdown/cleanup observation bound; never escalates to force")
	flag.Func("fixture-container", "exact terminal fixture ID carrying this runtime's ownership label (repeatable)", func(id string) error {
		cmd.Fixtures = append(cmd.Fixtures, id)
		return nil
	})
	return cmd
}
