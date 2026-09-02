package main

// CPUPlanningRequest is everything a planner may look at: one component's normalized
// requirements and the ledger that answers free-versus-taken for the deployment being
// planned. There is no deployment ID and no persisted map, and no context: planners
// perform no I/O.
type CPUPlanningRequest struct {
	Requirements NormalizedCPURequirements
	Ledger       *AllocationLedger
}

// CPUPlanner decides which CPU indices a component gets. Implementations are
// deterministic and side-effect free apart from reserving on the ledger.
type CPUPlanner interface {
	PlanCPU(CPUPlanningRequest) (CpuPlan, error)
}
