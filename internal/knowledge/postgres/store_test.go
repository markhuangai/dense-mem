package postgres

import (
	"testing"
)

var (
	_ RememberPort  = (*Store)(nil)
	_ LifecyclePort = (*Store)(nil)
)

func TestStoreExposesOnlyCapabilityPortsToComposition(t *testing.T) {
	store := NewStore(nil, nil)
	if store == nil {
		t.Fatal("NewStore returned nil")
	}

}
