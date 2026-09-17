package model

// one component's recorded cpu and cache allocation
type Reservation struct {
	Owner             OwnerRef
	Cpus              []int
	L3CacheAssignment *CacheAssignment
}

func (r Reservation) HasL3Cache() bool {
	return r.L3CacheAssignment != nil
}

func (r Reservation) CpuSet() string {
	return FormatCpuSet(r.Cpus)
}
