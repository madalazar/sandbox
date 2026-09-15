package controller

import (
	"context"
	"encoding/json"
	"maps"
	"reflect"
	"strings"
	"testing"

	"github.com/margo/sandbox/poc/device/agent/resource/model"
	"github.com/margo/sandbox/poc/device/agent/types"
)

type dummyIsolationController struct{}

func (d *dummyIsolationController) Apply(ctx context.Context, r model.Reservation) error {
	return nil
}

func (d *dummyIsolationController) Verify(ctx context.Context, r model.Reservation) error {
	return nil
}

func (d *dummyIsolationController) Release(ctx context.Context, r model.Reservation) error {
	return nil
}

func TestCacheIsolationControllerInterface(t *testing.T) {
	var _ CacheIsolationController = (*dummyIsolationController)(nil)

	ctrl := &dummyIsolationController{}
	ctx := context.Background()
	res := model.Reservation{}

	if err := ctrl.Apply(ctx, res); err != nil {
		t.Fatalf("unexpected Apply error: %v", err)
	}
	if err := ctrl.Verify(ctx, res); err != nil {
		t.Fatalf("unexpected Verify error: %v", err)
	}
	if err := ctrl.Release(ctx, res); err != nil {
		t.Fatalf("unexpected Release error: %v", err)
	}
}

// FakeDevice represents state of a fake hardware device for isolation contract testing
type FakeDevice interface {
	Snapshot() any
	Wipe()
}

func testIsolationContract(t *testing.T, createController func() (CacheIsolationController, FakeDevice, model.Reservation)) {
	t.Run("Release without Apply returns nil and leaves device untouched", func(t *testing.T) {
		ctrl, dev, res := createController()
		initialState := dev.Snapshot()
		if err := ctrl.Release(context.Background(), res); err != nil {
			t.Fatalf("Release without Apply error = %v, want nil", err)
		}
		if !reflect.DeepEqual(dev.Snapshot(), initialState) {
			t.Fatal("device state mutated after Release without Apply")
		}
	})

	t.Run("Apply then Release leaves device identical to state before Apply", func(t *testing.T) {
		ctrl, dev, res := createController()
		initialState := dev.Snapshot()
		if err := ctrl.Apply(context.Background(), res); err != nil {
			t.Fatalf("Apply error = %v", err)
		}
		if reflect.DeepEqual(dev.Snapshot(), initialState) {
			t.Fatal("Apply did not mutate device state")
		}
		if err := ctrl.Release(context.Background(), res); err != nil {
			t.Fatalf("Release error = %v", err)
		}
		if !reflect.DeepEqual(dev.Snapshot(), initialState) {
			t.Fatalf("state after Release %+v != initial state %+v", dev.Snapshot(), initialState)
		}
	})

	t.Run("Apply twice converges with no duplicate state", func(t *testing.T) {
		ctrl, dev, res := createController()
		if err := ctrl.Apply(context.Background(), res); err != nil {
			t.Fatalf("first Apply error = %v", err)
		}
		firstState := dev.Snapshot()
		if err := ctrl.Apply(context.Background(), res); err != nil {
			t.Fatalf("second Apply error = %v", err)
		}
		if !reflect.DeepEqual(dev.Snapshot(), firstState) {
			t.Fatalf("state after second Apply %+v != state after first Apply %+v", dev.Snapshot(), firstState)
		}
	})

	t.Run("Release twice returns nil", func(t *testing.T) {
		ctrl, _, res := createController()
		if err := ctrl.Apply(context.Background(), res); err != nil {
			t.Fatalf("Apply error = %v", err)
		}
		if err := ctrl.Release(context.Background(), res); err != nil {
			t.Fatalf("first Release error = %v", err)
		}
		if err := ctrl.Release(context.Background(), res); err != nil {
			t.Fatalf("second Release error = %v", err)
		}
	})

	t.Run("wipe device and Apply re-converges", func(t *testing.T) {
		ctrl, dev, res := createController()
		if err := ctrl.Apply(context.Background(), res); err != nil {
			t.Fatalf("Apply error = %v", err)
		}
		if err := ctrl.Verify(context.Background(), res); err != nil {
			t.Fatalf("Verify before wipe error = %v", err)
		}
		dev.Wipe()
		if err := ctrl.Apply(context.Background(), res); err != nil {
			t.Fatalf("re-Apply after wipe error = %v", err)
		}
		if err := ctrl.Verify(context.Background(), res); err != nil {
			t.Fatalf("Verify after re-Apply error = %v", err)
		}
	})

	t.Run("ClassUnset and no cache entries is a no-op", func(t *testing.T) {
		ctrl, dev, _ := createController()
		initialState := dev.Snapshot()
		emptyRes := model.Reservation{}
		if err := ctrl.Apply(context.Background(), emptyRes); err != nil {
			t.Fatalf("Apply emptyRes error = %v", err)
		}
		if err := ctrl.Verify(context.Background(), emptyRes); err != nil {
			t.Fatalf("Verify emptyRes error = %v", err)
		}
		if err := ctrl.Release(context.Background(), emptyRes); err != nil {
			t.Fatalf("Release emptyRes error = %v", err)
		}
		if !reflect.DeepEqual(dev.Snapshot(), initialState) {
			t.Fatal("device mutated after no-op reservation calls")
		}
	})
}

type fakePqosDevice struct {
	classes map[string]string
	cores   map[string]string
}

func newFakePqosDevice() *fakePqosDevice {
	return &fakePqosDevice{
		classes: make(map[string]string),
		cores:   make(map[string]string),
	}
}

func (d *fakePqosDevice) Snapshot() any {
	cl := make(map[string]string, len(d.classes))
	for k, v := range d.classes {
		cl[k] = v
	}
	co := make(map[string]string, len(d.cores))
	for k, v := range d.cores {
		co[k] = v
	}
	return struct {
		classes map[string]string
		cores   map[string]string
	}{classes: cl, cores: co}
}

func (d *fakePqosDevice) Wipe() {
	d.classes = make(map[string]string)
	d.cores = make(map[string]string)
}

func (d *fakePqosDevice) Run(ctx context.Context, command string, args ...string) ([]byte, error) {
	cmdStr := command + " " + strings.Join(args, " ")
	if strings.Contains(cmdStr, "core:0=") {
		// Reset: move cores back to COS 0, reset LLC mask to default
		for _, arg := range args {
			p := strings.Trim(arg, "'\"")
			if strings.HasPrefix(p, "llc@") {
				sub := strings.TrimPrefix(p, "llc@")
				subParts := strings.Split(sub, "=")
				if len(subParts) == 2 {
					idCos := strings.Split(subParts[0], ":")
					if len(idCos) == 2 {
						delete(d.classes, idCos[1])
						delete(d.cores, idCos[1])
					}
				}
			}
		}
		return []byte("reset"), nil
	}
	if strings.Contains(cmdStr, "llc@") && strings.Contains(cmdStr, "core:") {
		// Apply: parse llc@<id>:<cos>=<mask> and core:<cos>=<cpuset>
		for _, arg := range args {
			p := strings.Trim(arg, "'\"")
			if strings.HasPrefix(p, "llc@") {
				sub := strings.TrimPrefix(p, "llc@")
				subParts := strings.Split(sub, "=")
				if len(subParts) == 2 {
					idCos := strings.Split(subParts[0], ":")
					if len(idCos) == 2 {
						d.classes[idCos[1]] = subParts[1]
					}
				}
			}
			if strings.HasPrefix(p, "core:") {
				sub := strings.TrimPrefix(p, "core:")
				subParts := strings.Split(sub, "=")
				if len(subParts) == 2 {
					d.cores[subParts[0]] = subParts[1]
				}
			}
		}
		return []byte("applied"), nil
	}
	return []byte("ok"), nil
}

func TestPqosIsolationContract(t *testing.T) {
	caches := []types.HostTopologyCache{
		{Id: "0", Level: "L3", Ways: 12, WaySizeKB: 1024},
	}
	testIsolationContract(t, func() (CacheIsolationController, FakeDevice, model.Reservation) {
		dev := newFakePqosDevice()
		ctrl := NewPqosCacheController(dev, caches, 8)
		res := model.Reservation{
			Owner: model.NewOwnerRef("dep-1", "comp-a"),
			Cpus:  []int{2, 3},
			L3CacheAssignment: &model.CacheAssignment{
				CacheId: "0",
				Mask:    "0x3",
				Clos:    "1",
			},
		}
		return ctrl, dev, res
	})
}

type fakeRdtDevice struct {
	partitions map[string]any
	classes    map[string]any
}

func newFakeRdtDevice() *fakeRdtDevice {
	return &fakeRdtDevice{
		partitions: make(map[string]any),
		classes:    make(map[string]any),
	}
}

func (d *fakeRdtDevice) Snapshot() any {
	p := make(map[string]any, len(d.partitions))
	maps.Copy(p, d.partitions)
	c := make(map[string]any, len(d.classes))
	maps.Copy(c, d.classes)
	return struct {
		partitions map[string]any
		classes    map[string]any
	}{partitions: p, classes: c}
}

func (d *fakeRdtDevice) Wipe() {
	d.partitions = make(map[string]any)
	d.classes = make(map[string]any)
}

func (d *fakeRdtDevice) Parsed() *model.ParsedBalloonPolicy {
	parts := make(map[string]struct{}, len(d.partitions))
	for k := range d.partitions {
		parts[k] = struct{}{}
	}
	cls := make(map[string]struct{}, len(d.classes))
	for k := range d.classes {
		cls[k] = struct{}{}
	}
	return &model.ParsedBalloonPolicy{
		Name:      "default",
		Namespace: "kube-system",
		RdtConfig: model.RdtConfig{
			Partitions: parts,
			Classes:    cls,
		},
	}
}

func (d *fakeRdtDevice) Run(ctx context.Context, command string, args ...string) ([]byte, error) {
	cmdStr := command + " " + strings.Join(args, " ")
	_, after, ok := strings.Cut(cmdStr, "--patch ")
	if !ok {
		return []byte("ok"), nil
	}
	patchStr := after

	var patchMap map[string]any
	if err := json.Unmarshal([]byte(patchStr), &patchMap); err != nil {
		return nil, err
	}

	spec, _ := patchMap["spec"].(map[string]any)
	control, _ := spec["control"].(map[string]any)
	rdt, _ := control["rdt"].(map[string]any)

	if parts, ok := rdt["partitions"].(map[string]any); ok {
		for k, v := range parts {
			if v == nil {
				delete(d.partitions, k)
			} else {
				d.partitions[k] = v
				if pMap, ok := v.(map[string]any); ok {
					if cMap, ok := pMap["classes"].(map[string]any); ok {
						maps.Copy(d.classes, cMap)
					}
				}
			}
		}
	}
	if cls, ok := rdt["classes"].(map[string]any); ok {
		for k, v := range cls {
			if v == nil {
				delete(d.classes, k)
			} else {
				d.classes[k] = v
			}
		}
	}

	return []byte("patched"), nil
}

func TestRdtIsolationContract(t *testing.T) {
	caches := []types.HostTopologyCache{
		{Id: "0", Level: "L3", Ways: 12, WaySizeKB: 1024},
	}
	testIsolationContract(t, func() (CacheIsolationController, FakeDevice, model.Reservation) {
		dev := newFakeRdtDevice()
		ctrl := NewRdtPolicyControllerWithReader(dev, dev, caches)
		res := model.Reservation{
			Owner: model.NewOwnerRef("dep-1", "comp-a"),
			Cpus:  []int{2, 3},
			L3CacheAssignment: &model.CacheAssignment{
				CacheId: "0",
				Mask:    "0x3",
				Clos:    "comp-a_class",
			},
			Clos: "comp-a_class",
		}
		return ctrl, dev, res
	})
}
