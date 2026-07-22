package dreamservice

import (
	"context"

	"github.com/markhuangai/dense-mem/internal/domain"
)

type cycleAppConfigStub struct {
	cfg domain.DreamingRuntimeConfig
}

func (s cycleAppConfigStub) DreamingRuntimeConfig(context.Context) (domain.DreamingRuntimeConfig, error) {
	return s.cfg, nil
}

type dreamGeneratorStub struct {
	model     string
	generated []GeneratedDream
	err       error
	lastReq   GenerateRequest
	calls     int
}

func (s *dreamGeneratorStub) Generate(_ context.Context, _ string, req GenerateRequest) ([]GeneratedDream, error) {
	s.calls++
	s.lastReq = req
	return s.generated, s.err
}

func (s *dreamGeneratorStub) Model() string {
	if s.model != "" {
		return s.model
	}
	return "stub-model"
}

type testStringer string

func (s testStringer) String() string {
	return string(s)
}
