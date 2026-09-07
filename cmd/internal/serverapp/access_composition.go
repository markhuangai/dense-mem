package serverapp

import (
	"github.com/markhuangai/dense-mem/internal/crypto"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/service"
	accessservice "github.com/markhuangai/dense-mem/internal/service/access"
)

type accessApplicationDependencies struct {
	TeamRepo              accessservice.TeamStore
	CredentialRepo        accessservice.CredentialStore
	ActiveCredentialRepo  accessservice.ActiveCredentialRepository
	CredentialActivity    accessservice.CredentialActivityStore
	CredentialBatch       credentialLastUsedBatchRepository
	SSORepo               accessservice.SSOStore
	PortalSessionRepo     accessservice.PortalSessionStore
	DirectoryIdentityRepo accessservice.DirectoryIdentityStore
	ControlIdentityRepo   accessservice.ControlIdentityStore
	CredentialVerifier    crypto.CredentialVerifier
	ActivityWriter        *accessservice.CredentialActivityWriter
	AuthVerifyConcurrency int
	Audit                 service.AuditService
	RuntimeConfig         accessservice.SSORuntimeConfigProvider
	Logger                observability.LogProvider
	StatePurger           accessservice.TeamStatePurger
	SessionInvalidator    accessservice.CredentialSessionInvalidator
}

type accessAuthenticationApplication struct {
	CredentialVerifier crypto.CredentialVerifier
	ActivityWriter     *accessservice.CredentialActivityWriter
}

func buildAccessAuthenticationApplication(
	credentialRepo accessservice.CredentialActivityStore,
	credentialBatch credentialLastUsedBatchRepository,
	verifyConcurrency int,
	logger observability.LogProvider,
) accessAuthenticationApplication {
	return accessAuthenticationApplication{
		CredentialVerifier: crypto.NewArgon2Verifier(verifyConcurrency),
		ActivityWriter: accessservice.NewCredentialActivityWriterWithBatch(
			credentialRepo,
			newCredentialActivityBatchAdapter(credentialBatch),
			logger,
		),
	}
}

type accessApplication struct {
	CredentialVerifier   crypto.CredentialVerifier
	ActivityWriter       *accessservice.CredentialActivityWriter
	TeamService          *accessservice.TeamServiceImpl
	CredentialService    *accessservice.CredentialServiceImpl
	SSOService           *accessservice.SSOService
	PortalSessionService *accessservice.UserPortalSessionService
	DirectoryIdentity    *accessservice.DirectoryIdentityService
	ControlIdentity      *accessservice.ControlIdentityService
}

func buildAccessApplication(deps accessApplicationDependencies) accessApplication {
	credentialVerifier := deps.CredentialVerifier
	if credentialVerifier == nil {
		credentialVerifier = crypto.NewArgon2Verifier(deps.AuthVerifyConcurrency)
	}
	activityWriter := deps.ActivityWriter
	if activityWriter == nil {
		activityWriter = accessservice.NewCredentialActivityWriterWithBatch(
			deps.CredentialActivity,
			newCredentialActivityBatchAdapter(deps.CredentialBatch),
			deps.Logger,
		)
	}
	teamService := accessservice.NewTeamService(deps.TeamRepo, deps.Audit, deps.StatePurger)
	credentialService := accessservice.NewCredentialService(deps.CredentialRepo, teamService, deps.Audit, deps.SessionInvalidator)
	ssoService := accessservice.NewSSOService(deps.SSORepo, accessservice.SSOConfig{
		RuntimeConfig: deps.RuntimeConfig,
		Logger:        deps.Logger,
	})
	return accessApplication{
		CredentialVerifier:   credentialVerifier,
		ActivityWriter:       activityWriter,
		TeamService:          teamService,
		CredentialService:    credentialService,
		SSOService:           ssoService,
		PortalSessionService: accessservice.NewUserPortalSessionService(deps.PortalSessionRepo, deps.ActiveCredentialRepo, nil),
		DirectoryIdentity: accessservice.NewDirectoryIdentityService(deps.DirectoryIdentityRepo, accessservice.DirectoryIdentityConfig{
			CredentialVerifier: credentialVerifier,
		}),
		ControlIdentity: accessservice.NewControlIdentityService(deps.ControlIdentityRepo, deps.SSORepo, accessservice.ControlIdentityConfig{
			RuntimeConfig: deps.RuntimeConfig,
		}),
	}
}
