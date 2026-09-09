// Package communityservice preserves the historical Community application
// import path while policy and scheduling live in internal/community/service.
package communityservice

import (
	"log/slog"

	communityapp "github.com/markhuangai/dense-mem/internal/community/service"
)

type (
	AppConfig       = communityapp.AppConfig
	TeamService     = communityapp.TeamService
	SummaryProvider = communityapp.SummaryProvider
	Dependencies    = communityapp.Dependencies
	Service         = communityapp.Service
	RunResult       = communityapp.RunResult
	StatusResult    = communityapp.StatusResult
	Scheduler       = communityapp.Scheduler
)

var (
	New          = communityapp.New
	NewScheduler = func(service Service, teams TeamService, config AppConfig, logger *slog.Logger) *Scheduler {
		return communityapp.NewScheduler(service, teams, config, logger)
	}
)
