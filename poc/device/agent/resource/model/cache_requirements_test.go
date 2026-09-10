package model

import (
	"strings"
	"testing"

	"github.com/margo/sandbox/standard/generatedCode/wfm/sbi"
)

func strPtr(s string) *string {
	return &s
}

func TestNormalizeCacheRequirements(t *testing.T) {
	t.Run("nil or empty requirements", func(t *testing.T) {
		normalized, err := NormalizeCacheRequirements("comp-a", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if normalized.HasCache() || normalized.L3CacheRequirement != nil {
			t.Fatal("expected no cache requirements")
		}

		empty := &sbi.RequiredResources{Cache: &[]sbi.Cache{}}
		normalized, err = NormalizeCacheRequirements("comp-a", empty)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if normalized.HasCache() || normalized.L3CacheRequirement != nil {
			t.Fatal("expected no cache requirements")
		}
	})

	t.Run("valid L3 exclusive requirement", func(t *testing.T) {
		req := &sbi.RequiredResources{
			Cache: &[]sbi.Cache{
				{
					Level:      sbi.CacheLevelL3,
					Allocation: sbi.CacheAllocationExclusive,
					Size:       strPtr("9216Ki"),
				},
			},
		}

		normalized, err := NormalizeCacheRequirements("comp-a", req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !normalized.HasCache() || normalized.L3CacheRequirement == nil {
			t.Fatal("expected L3 cache requirement to be present")
		}
		if normalized.L3CacheRequirement.SizeKiB != 9216 {
			t.Fatalf("expected SizeKiB == 9216, got %d", normalized.L3CacheRequirement.SizeKiB)
		}
		if normalized.L3CacheRequirement.Level != "L3" || normalized.L3CacheRequirement.Allocation != CacheAllocationExclusive {
			t.Fatalf("unexpected requirement fields: %+v", *normalized.L3CacheRequirement)
		}
	})

	t.Run("rejects non-L3 level", func(t *testing.T) {
		levels := []sbi.CacheLevel{sbi.CacheLevelL1d, sbi.CacheLevelL1i, sbi.CacheLevelL2}
		for _, level := range levels {
			req := &sbi.RequiredResources{
				Cache: &[]sbi.Cache{
					{
						Level:      level,
						Allocation: sbi.CacheAllocationExclusive,
						Size:       strPtr("1024Ki"),
					},
				},
			}
			_, err := NormalizeCacheRequirements("comp-a", req)
			if err == nil {
				t.Fatalf("expected error for level %s, got nil", level)
			}
			if !strings.Contains(strings.ToLower(err.Error()), "only l3 cache is supported") {
				t.Fatalf("unexpected error message: %v", err)
			}
		}
	})

	t.Run("skips shared cache allocation", func(t *testing.T) {
		req := &sbi.RequiredResources{
			Cache: &[]sbi.Cache{
				{
					Level:      sbi.CacheLevelL3,
					Allocation: sbi.CacheAllocationShared,
					Size:       strPtr("1024Ki"),
				},
			},
		}
		normalized, err := NormalizeCacheRequirements("comp-a", req)
		if err != nil {
			t.Fatalf("unexpected error for shared cache allocation: %v", err)
		}
		if normalized.HasCache() {
			t.Fatal("expected shared cache allocation to be skipped")
		}
	})

	t.Run("rejects multiple L3 cache requirements", func(t *testing.T) {
		req := &sbi.RequiredResources{
			Cache: &[]sbi.Cache{
				{
					Level:      sbi.CacheLevelL3,
					Allocation: sbi.CacheAllocationExclusive,
					Size:       strPtr("1024Ki"),
				},
				{
					Level:      sbi.CacheLevelL3,
					Allocation: sbi.CacheAllocationExclusive,
					Size:       strPtr("2048Ki"),
				},
			},
		}
		_, err := NormalizeCacheRequirements("comp-a", req)
		if err == nil {
			t.Fatal("expected error for multiple L3 requirements, got nil")
		}
		if !strings.Contains(strings.ToLower(err.Error()), "only one") && !strings.Contains(strings.ToLower(err.Error()), "multiple") {
			t.Fatalf("unexpected error message: %v", err)
		}
	})

	t.Run("rejects invalid size", func(t *testing.T) {
		req := &sbi.RequiredResources{
			Cache: &[]sbi.Cache{
				{
					Level:      sbi.CacheLevelL3,
					Allocation: sbi.CacheAllocationExclusive,
					Size:       strPtr("invalid"),
				},
			},
		}
		_, err := NormalizeCacheRequirements("comp-a", req)
		if err == nil {
			t.Fatal("expected error for invalid size, got nil")
		}
	})
}

func TestParseBinarySizeKi(t *testing.T) {
	tests := []struct {
		name      string
		raw       *string
		expected  int64
		expectErr bool
	}{
		{name: "nil", raw: nil, expectErr: true},
		{name: "empty", raw: strPtr(""), expectErr: true},
		{name: "whitespace", raw: strPtr("   "), expectErr: true},
		{name: "kibibytes", raw: strPtr("2048Ki"), expected: 2048},
		{name: "kibibytes with space", raw: strPtr("2048 Ki"), expected: 2048},
		{name: "mebibytes", raw: strPtr("2Mi"), expected: 2048},
		{name: "gibibytes", raw: strPtr("1Gi"), expected: 1024 * 1024},
		{name: "fractional mebibytes", raw: strPtr("1.5Mi"), expected: 1536},
		{name: "non-numeric", raw: strPtr("abcKi"), expectErr: true},
		{name: "zero", raw: strPtr("0Ki"), expectErr: true},
		{name: "negative", raw: strPtr("-5Mi"), expectErr: true},
		{name: "unsupported unit", raw: strPtr("100KB"), expectErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseBinarySizeKi(tc.raw)
			if tc.expectErr {
				if err == nil {
					t.Fatalf("expected error for %v, got nil", tc.raw)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %v: %v", tc.raw, err)
			}
			if got != tc.expected {
				t.Fatalf("expected %d, got %d", tc.expected, got)
			}
		})
	}
}
