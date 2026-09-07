package contract

import (
	"reflect"
	"testing"
)

func TestInputDoesNotExposeDerivedSpaceScope(t *testing.T) {
	if _, ok := reflect.TypeOf(Input{}).FieldByName("spaceID"); ok {
		t.Fatal("trace input must not expose adapter-derived space scope")
	}
}
