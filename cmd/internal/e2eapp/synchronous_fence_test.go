package e2eapp

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSynchronousFenceFaultSelectionUsesOnlyBootConfiguration(t *testing.T) {
	for _, test := range []struct {
		name  string
		value string
		want  string
	}{
		{name: "search generation", value: " search-generation-rotation ", want: synchronousSearchGenerationFault},
		{name: "supersession", value: "supersession-rotation", want: synchronousSupersessionFault},
		{name: "request marker", value: "[fixture-fault:search-generation-rotation]", want: ""},
		{name: "unknown", value: "unexpected", want: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, synchronousFenceFaultSelection(test.value))
		})
	}
}
