package resource

import "testing"

func TestSanitizeFileToken(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", "unknown"},
		{"   ", "unknown"},
		{"simple", "simple"},
		{"cyclictest_compose", "cyclictest_compose"},
		{"foo/bar/baz", "foo-bar-baz"},
		{"deployment#123!@$", "deployment-123"},
		{"---", "unknown"},
		{"-valid-name-", "valid-name"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := SanitizeFileToken(tt.input); got != tt.want {
				t.Errorf("SanitizeFileToken(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
