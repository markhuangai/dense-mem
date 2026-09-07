package contract

import (
	"reflect"
	"testing"
)

func TestQueryDoesNotExposeDerivedSpaceScope(t *testing.T) {
	if _, ok := reflect.TypeOf(Query{}).FieldByName("spaceID"); ok {
		t.Fatal("graph query must not expose adapter-derived space scope")
	}
}
