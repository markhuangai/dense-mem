package registry

import "testing"

func TestBuildActiveDoesNotRegisterSubmissionStatus(t *testing.T) {
	reg, err := BuildActive(Dependencies{})
	if err != nil {
		t.Fatalf("BuildActive: %v", err)
	}
	if _, ok := reg.Get("get_submission_status"); ok {
		t.Fatal("removed get_submission_status tool remains registered")
	}
}
func TestBuildActiveDoesNotRegisterRemovedPlacementTools(t *testing.T) {
	reg, err := BuildActive(Dependencies{})
	if err != nil {
		t.Fatalf("BuildActive: %v", err)
	}
	for _, name := range []string{"get_memory_placement", "resolve_memory_placement"} {
		if _, ok := reg.Get(name); ok {
			t.Fatalf("removed tool %s remains registered", name)
		}
	}
}
