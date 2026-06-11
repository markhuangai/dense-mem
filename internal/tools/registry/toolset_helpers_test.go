package registry

import "testing"

func TestToolsetHelperBranches(t *testing.T) {
	if got, ok := intInput(int64(7)); !ok || got != 7 {
		t.Fatalf("intInput int64 = %d, %v; want 7, true", got, ok)
	}
	if got, ok := intInput(float64(8)); !ok || got != 8 {
		t.Fatalf("intInput float64 = %d, %v; want 8, true", got, ok)
	}
	if _, ok := intInput(float64(8.5)); ok {
		t.Fatal("intInput non-integral float ok = true, want false")
	}
	if _, ok := intInput("8"); ok {
		t.Fatal("intInput string ok = true, want false")
	}
	if err := remapInput(map[string]any{"bad": func() {}}, &struct{}{}); err == nil {
		t.Fatal("remapInput with unmarshalable value: want error")
	}
	if _, err := structToMap(map[string]any{"bad": func() {}}); err == nil {
		t.Fatal("structToMap with unmarshalable value: want error")
	}
	args := map[string]any{"team_id": "team-1", "profile_id": "profile-1", "query": "keep"}
	StripTenantOverrideArgs(args)
	if _, ok := args["team_id"]; ok {
		t.Fatal("StripTenantOverrideArgs kept team_id")
	}
	if _, ok := args["profile_id"]; ok {
		t.Fatal("StripTenantOverrideArgs kept profile_id")
	}
	if args["query"] != "keep" {
		t.Fatalf("StripTenantOverrideArgs query = %v, want keep", args["query"])
	}
}
