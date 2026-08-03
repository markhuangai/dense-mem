package evalharness

import "testing"

func TestNormalizeImportConcurrency(t *testing.T) {
	tests := []struct {
		input   int
		want    int
		wantErr bool
	}{
		{input: 0, want: DefaultImportConcurrency},
		{input: 1, want: 1},
		{input: MaxImportConcurrency, want: MaxImportConcurrency},
		{input: MaxImportConcurrency + 1, wantErr: true},
	}
	for _, tt := range tests {
		got, err := normalizeImportConcurrency(tt.input)
		if tt.wantErr {
			if err == nil {
				t.Fatalf("normalizeImportConcurrency(%d) error = nil", tt.input)
			}
			continue
		}
		if err != nil {
			t.Fatalf("normalizeImportConcurrency(%d): %v", tt.input, err)
		}
		if got != tt.want {
			t.Fatalf("normalizeImportConcurrency(%d) = %d, want %d", tt.input, got, tt.want)
		}
	}
}
