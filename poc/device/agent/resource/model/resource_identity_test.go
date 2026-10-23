package model

import "testing"

func TestOwnerRefString(t *testing.T) {
	tests := []struct {
		name  string
		owner OwnerRef
		want  string
	}{
		{
			name:  "deployment and component",
			owner: NewOwnerRef("deployment-1", "component-a"),
			want:  "deployment-1/component-a",
		},
		{
			name:  "component omitted encodes the bare deployment",
			owner: NewOwnerRef("deployment-1", ""),
			want:  "deployment-1",
		},
		{
			name:  "surrounding whitespace is trimmed",
			owner: NewOwnerRef("  deployment-1 ", " component-a  "),
			want:  "deployment-1/component-a",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.owner.String(); got != test.want {
				t.Fatalf("String() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestOwnerRefCanTake(t *testing.T) {
	claimant := NewOwnerRef("deployment-1", "component-a")

	tests := []struct {
		name   string
		holder OwnerRef
		want   bool
	}{
		{name: "unheld resource", holder: OwnerRef{}, want: true},
		{name: "own claim is reusable", holder: NewOwnerRef("deployment-1", "component-a"), want: true},
		{name: "bare deployment claim of own deployment", holder: NewOwnerRef("deployment-1", ""), want: true},
		{name: "sibling component blocks", holder: NewOwnerRef("deployment-1", "component-b"), want: false},
		{name: "another deployment blocks", holder: NewOwnerRef("deployment-2", "component-a"), want: false},
		{name: "bare claim of another deployment blocks", holder: NewOwnerRef("deployment-2", ""), want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := claimant.CanTake(test.holder); got != test.want {
				t.Fatalf("CanTake(%q) = %v, want %v", test.holder.String(), got, test.want)
			}
		})
	}
}

func TestParseOwnerRef(t *testing.T) {
	tests := []struct {
		name  string
		owner string
		want  OwnerRef
	}{
		{
			name:  "deployment and component",
			owner: "deployment-1/component-a",
			want:  OwnerRef{Deployment: "deployment-1", Component: "component-a"},
		},
		{
			name:  "bare deployment",
			owner: "deployment-1",
			want:  OwnerRef{Deployment: "deployment-1"},
		},
		{
			name:  "empty owner",
			owner: "",
			want:  OwnerRef{},
		},
		{
			name:  "surrounding whitespace is trimmed",
			owner: "  deployment-1 / component-a  ",
			want:  OwnerRef{Deployment: "deployment-1", Component: "component-a"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ParseOwnerRef(test.owner); got != test.want {
				t.Fatalf("ParseOwnerRef(%q) = %+v, want %+v", test.owner, got, test.want)
			}
		})
	}
}

func TestParseOwnerRefRoundTrip(t *testing.T) {
	for _, owner := range []OwnerRef{
		NewOwnerRef("deployment-1", "component-a"),
		NewOwnerRef("deployment-1", ""),
	} {
		if got := ParseOwnerRef(owner.String()); got != owner {
			t.Fatalf("ParseOwnerRef(%q) = %+v, want %+v", owner.String(), got, owner)
		}
	}
}
