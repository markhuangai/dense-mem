package postgres

import (
	"testing"

	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
)

var (
	_ RememberPort  = (*Store)(nil)
	_ LifecyclePort = (*Store)(nil)
	_ ConflictPort  = (*Store)(nil)
)

func TestStoreExposesOnlyCapabilityPortsToComposition(t *testing.T) {
	store := NewStore(nil, nil, knowledgecontract.ConflictRuntimeConfig{})
	if store == nil {
		t.Fatal("NewStore returned nil")
	}

}
