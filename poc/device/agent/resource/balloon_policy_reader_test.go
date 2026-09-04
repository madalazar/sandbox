package resource

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/margo/sandbox/poc/device/agent/resource/model"
)

func TestParseBalloonsPolicy(t *testing.T) {
	trueVal := true
	minCpus := int64(1)
	maxCpus := int64(2)

	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "config.nri/v1alpha1",
			"kind":       "BalloonsPolicy",
			"metadata": map[string]interface{}{
				"name":      "default-balloons",
				"namespace": "kube-system",
			},
			"spec": map[string]interface{}{
				"config": map[string]interface{}{
					"balloonTypes": []interface{}{
						map[string]interface{}{
							"name":                 "isolated-balloon",
							"preferCoreType":       "performance",
							"preferIsolCpus":       true,
							"minCPUs":              1,
							"maxCPUs":              int64(2),
							"preferCloseToDevices": []interface{}{"/dev/vfio/0"},
						},
					},
				},
			},
		},
	}

	policy, err := parseBalloonsPolicy(obj)
	if err != nil {
		t.Fatalf("parseBalloonsPolicy() error = %v", err)
	}

	if policy.Name != "default-balloons" || policy.Namespace != "kube-system" {
		t.Fatalf("policy name/namespace = %q/%q, want default-balloons/kube-system", policy.Name, policy.Namespace)
	}

	if len(policy.BalloonTypes) != 1 {
		t.Fatalf("balloon types count = %d, want 1", len(policy.BalloonTypes))
	}

	bt := policy.BalloonTypes[0]
	if bt.Name != "isolated-balloon" {
		t.Errorf("bt.Name = %q, want isolated-balloon", bt.Name)
	}
	if bt.PreferCoreType != "performance" {
		t.Errorf("bt.PreferCoreType = %q, want performance", bt.PreferCoreType)
	}
	if bt.PreferIsolCpus == nil || *bt.PreferIsolCpus != trueVal {
		t.Errorf("bt.PreferIsolCpus = %v, want true", bt.PreferIsolCpus)
	}
	if bt.MinCpus == nil || *bt.MinCpus != minCpus {
		t.Errorf("bt.MinCpus = %v, want %d", bt.MinCpus, minCpus)
	}
	if bt.MaxCpus == nil || *bt.MaxCpus != maxCpus {
		t.Errorf("bt.MaxCpus = %v, want %d", bt.MaxCpus, maxCpus)
	}
	if !reflect.DeepEqual(bt.PreferCloseToDevices, []string{"/dev/vfio/0"}) {
		t.Errorf("bt.PreferCloseToDevices = %v, want [/dev/vfio/0]", bt.PreferCloseToDevices)
	}
}

func TestParseBalloonsPolicyErrors(t *testing.T) {
	if _, err := parseBalloonsPolicy(nil); err == nil {
		t.Fatal("expected error for nil object, got nil")
	}

	objWithoutConfig := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "config.nri/v1alpha1",
		},
	}
	if _, err := parseBalloonsPolicy(objWithoutConfig); err == nil {
		t.Fatal("expected error for missing config section, got nil")
	}

	// Balloon type has empty name
	objEmptyName := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"config": map[string]interface{}{
				"balloonTypes": []interface{}{
					map[string]interface{}{"name": "   "},
				},
			},
		},
	}
	if _, err := parseBalloonsPolicy(objEmptyName); err == nil {
		t.Fatal("expected error for empty balloon name, got nil")
	}

	// Non-map item in balloonTypes
	objNonMap := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"config": map[string]interface{}{
				"balloonTypes": []interface{}{"not-a-map"},
			},
		},
	}
	if _, err := parseBalloonsPolicy(objNonMap); err == nil {
		t.Fatal("expected error for non-map balloonType item, got nil")
	}

	// Negative minCPUs
	objNegativeMin := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"config": map[string]interface{}{
				"balloonTypes": []interface{}{
					map[string]interface{}{"name": "b1", "minCPUs": -1},
				},
			},
		},
	}
	if _, err := parseBalloonsPolicy(objNegativeMin); err == nil {
		t.Fatal("expected error for negative minCPUs, got nil")
	}

	// minCPUs > maxCPUs
	objMinGreaterThanMax := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"config": map[string]interface{}{
				"balloonTypes": []interface{}{
					map[string]interface{}{"name": "b1", "minCPUs": 4, "maxCPUs": 2},
				},
			},
		},
	}
	if _, err := parseBalloonsPolicy(objMinGreaterThanMax); err == nil {
		t.Fatal("expected error for minCPUs > maxCPUs, got nil")
	}
}

func TestAsInt64(t *testing.T) {
	tests := []struct {
		input interface{}
		want  int64
		ok    bool
	}{
		{int(5), 5, true},
		{int8(6), 6, true},
		{int16(7), 7, true},
		{int32(10), 10, true},
		{int64(15), 15, true},
		{uint(8), 8, true},
		{uint8(9), 9, true},
		{uint16(10), 10, true},
		{uint32(11), 11, true},
		{uint64(12), 12, true},
		{uint64(math.MaxUint64), 0, false},
		{float32(20), 20, true},
		{float64(25), 25, true},
		{float64(2.5), 0, false},
		{json.Number("42"), 42, true},
		{"100", 100, true},
		{"not-a-number", 0, false},
		{nil, 0, false},
	}

	for _, tt := range tests {
		got, ok := asInt64(tt.input)
		if ok != tt.ok || got != tt.want {
			t.Errorf("asInt64(%v) = (%d, %v), want (%d, %v)", tt.input, got, ok, tt.want, tt.ok)
		}
	}
}

func TestBalloonPolicyInformerStopMultipleCalls(t *testing.T) {
	informer := &BalloonPolicyInformer{
		stopCh: make(chan struct{}),
	}
	// Calling Stop multiple times must not panic
	informer.Stop()
	informer.Stop()
}

func TestParsedImmutability(t *testing.T) {
	minCpus := int64(1)
	maxCpus := int64(2)
	preferIsol := true

	informer := &BalloonPolicyInformer{}
	original := &model.ParsedBalloonPolicy{
		Name:      "original-policy",
		Namespace: "kube-system",
		BalloonTypes: []model.ParsedBalloonType{
			{
				Name:                 "balloon-1",
				PreferCoreType:       "performance",
				PreferIsolCpus:       &preferIsol,
				MinCpus:              &minCpus,
				MaxCpus:              &maxCpus,
				PreferCloseToDevices: []string{"/dev/vfio/0"},
			},
		},
	}
	informer.cache.Store(original)

	// Obtain copy and mutate it
	copy1 := informer.Parsed()
	if copy1 == nil {
		t.Fatal("expected non-nil copy")
	}
	copy1.Name = "mutated-policy"
	copy1.BalloonTypes[0].Name = "mutated-balloon"
	*copy1.BalloonTypes[0].MinCpus = 999
	copy1.BalloonTypes[0].PreferCloseToDevices[0] = "/dev/vfio/mutated"
	copy1.BalloonTypes[0].PreferCloseToDevices = append(copy1.BalloonTypes[0].PreferCloseToDevices, "/dev/vfio/1")

	// Obtain second copy and verify it wasn't mutated
	copy2 := informer.Parsed()
	if copy2.Name != "original-policy" {
		t.Errorf("copy2.Name = %q, want original-policy", copy2.Name)
	}
	if copy2.BalloonTypes[0].Name != "balloon-1" {
		t.Errorf("copy2.BalloonTypes[0].Name = %q, want balloon-1", copy2.BalloonTypes[0].Name)
	}
	if *copy2.BalloonTypes[0].MinCpus != 1 {
		t.Errorf("*copy2.BalloonTypes[0].MinCpus = %d, want 1", *copy2.BalloonTypes[0].MinCpus)
	}
	if !reflect.DeepEqual(copy2.BalloonTypes[0].PreferCloseToDevices, []string{"/dev/vfio/0"}) {
		t.Errorf("copy2.PreferCloseToDevices = %v, want [/dev/vfio/0]", copy2.BalloonTypes[0].PreferCloseToDevices)
	}
}
