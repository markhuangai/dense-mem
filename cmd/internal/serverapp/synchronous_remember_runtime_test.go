package serverapp

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/assessor"
)

func TestInstallSynchronousRememberFactoryBuildsLazyService(t *testing.T) {
	writeRuntime := &WriteRuntime{}
	installSynchronousRememberFactory(writeRuntime, nil, nil, nil, assessor.SemanticAssessmentLimits{}, nil, nil, nil, nil, nil)

	require.NotNil(t, writeRuntime.SynchronousRememberFactory)
	require.NotNil(t, writeRuntime.SynchronousRememberFactory())
}
