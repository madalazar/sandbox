package resource

import "testing"

func TestAllocationLedgerIsCpuAvailable(t *testing.T) {
	isolated := map[int]struct{}{1: {}, 2: {}, 3: {}, 4: {}}
	snapshot := NewAllocationSnapshot(map[int]string{
		1: "deployment-1/component-a",
		2: "deployment-1/component-b",
		3: "deployment-2/component-a",
		5: "deployment-2/component-a", // not isolated; never allocatable
	}, isolated)

	tests := []struct {
		name     string
		cpuIndex int
		want     bool
	}{
		{name: "unheld index is free", cpuIndex: 4, want: true},
		{name: "own persisted claim is reusable", cpuIndex: 1, want: true},
		{name: "sibling component blocks", cpuIndex: 2, want: false},
		{name: "another deployment blocks", cpuIndex: 3, want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ledger := NewAllocationLedger(snapshot, "deployment-1")
			if got := ledger.IsCpuAvailable(test.cpuIndex, "component-a"); got != test.want {
				t.Fatalf("IsCpuAvailable(%d) = %v, want %v", test.cpuIndex, got, test.want)
			}
		})
	}
}

func TestAllocationLedgerReserveBlocksSiblingsInSamePass(t *testing.T) {
	ledger := NewAllocationLedger(NewAllocationSnapshot(nil, nil), "deployment-1")

	if err := ledger.ReserveCpus("component-a", []int{4}); err != nil {
		t.Fatalf("ReserveCpus() error = %v", err)
	}

	if ledger.IsCpuAvailable(4, "component-b") {
		t.Fatal("IsCpuAvailable() = true for a cpu reserved earlier in this pass")
	}
	if !ledger.IsCpuAvailable(4, "component-a") {
		t.Fatal("IsCpuAvailable() = false for the component that reserved the cpu")
	}
	if err := ledger.ReserveCpus("component-b", []int{4}); err == nil {
		t.Fatal("ReserveCpus() = nil, want an error for an unavailable cpu")
	}
}

func TestAllocationLedgerReserveIsAllOrNothing(t *testing.T) {
	ledger := NewAllocationLedger(NewAllocationSnapshot(nil, nil), "deployment-1")

	if err := ledger.ReserveCpus("component-a", []int{1}); err != nil {
		t.Fatalf("ReserveCpus() error = %v", err)
	}
	if err := ledger.ReserveCpus("component-b", []int{2, 1}); err == nil {
		t.Fatal("ReserveCpus() = nil, want an error")
	}
	if !ledger.IsCpuAvailable(2, "component-c") {
		t.Fatal("IsCpuAvailable(2) = false; a failed reservation claimed an index")
	}
}
