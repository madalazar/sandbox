# Resource Assignment Refactor - Structure and Sequence Diagrams

Companion to [resource-assignment-refactor-plan.md](resource-assignment-refactor-plan.md). Nothing
here is new design; it is the plan's §2-§11 drawn out. Section references point back at the source
of each element.

## 1. Domain Model (§3, §3.1, §4, §4.1)

Value types only - no I/O, no technology. `ResourcePlan` is what was *decided*; `Reservation` is
what was *recorded* and is the only thing that survives the process.

```mermaid
classDiagram
    direction LR

    class RequirementRef {
        <<string>>
        +componentName
    }

    class OwnerRef {
        +string Deployment
        +RequirementRef Ref
    }

    class ClassID {
        <<string>>
        +Held() bool
        +String() string
    }

    class WayInterval {
        +int64 Start
        +int64 Length
    }

    class ResourceRequest {
        +OwnerRef Owner
        +RequiredResources Requirements
    }

    class ResourcePlan {
        +OwnerRef Owner
        +CPUPlan CPU
        +CachePlan Cache
        +HasCPU() bool
        +HasCache() bool
        +CPUSet() string
        +Reservation() Reservation
    }

    class CPUPlan {
        +Map~RequirementRef, CPUList~ Assignments
        +CPUPlacement Placement
    }

    class CPUPlacement {
        +string Class
    }

    class CachePlan {
        +List~CacheAllocation~ Assignments
        +ClassID Class
    }

    class CacheAllocation {
        +RequirementRef Ref
        +string Level
        +string CacheID
        +int64 SizeKiB
        +WayInterval Interval
        +string Mask
    }

    class Reservation {
        +OwnerRef Owner
        +List~int~ CPUs
        +List~CacheReservation~ Caches
        +ClassID Class
        +HasCache() bool
        +CPUSet() string
    }

    class CacheReservation {
        +OwnerRef Owner
        +string Level
        +string CacheID
        +int64 SizeKiB
        +WayInterval Interval
        +ClassID Class
    }

    class CPURequirement {
        +string Name
        +CPUMode Mode
        +int Cores
    }

    class CacheRequirement {
        +string Name
        +string Level
        +CacheAllocationMode Allocation
        +int64 SizeKiB
    }

    OwnerRef *-- RequirementRef
    ResourceRequest *-- OwnerRef
    ResourcePlan *-- OwnerRef
    ResourcePlan *-- CPUPlan
    ResourcePlan *-- CachePlan
    CPUPlan *-- CPUPlacement
    CachePlan *-- CacheAllocation
    CachePlan *-- ClassID
    CacheAllocation *-- WayInterval
    Reservation *-- OwnerRef
    Reservation *-- ClassID
    Reservation *-- CacheReservation
    CacheReservation *-- WayInterval
    CacheReservation *-- ClassID
    ResourcePlan ..> Reservation : projects
```

- `RequirementRef` is `components[].name`, trimmed - the whole identity (§4.1).
- `ClassID` is opaque: `""` is unset, `"0"` is COS 0 (R1.47).
- `CPUPlacement.Class` holds a balloon name, empty for direct pinning.
- `CachePlan.Class` is reserved during `PlanCache` and is `ClassUnset` when no cache is requested.
- `ResourceRequest` deliberately carries no allocation state.

## 2. Allocation State (§3.1)

The split R1.4 asked for: an immutable read, and a mutable per-deployment accumulator. The ledger
is the **single allocator** for every scarce pool - isolated CPUs, cache ways, class slots. The
`ClassNamer` is *not* a second allocator: the ledger grants the slot and enforces the cap, and the
namer only supplies the runtime's spelling (R1.10, R1.47).

```mermaid
classDiagram
    direction TB

    class AllocationSnapshot {
        <<immutable>>
        +Map~int, OwnerRef~ CPUOwners
        +List~CacheReservation~ Caches
    }

    class AllocationLedger {
        <<mutable>>
        -AllocationSnapshot snapshot
        -OwnerRef self
        -CacheCapacity caps
        -ClassNamer classes
        -Map~int, RequirementRef~ reservedCPUs
        -Map~string, WayIntervals~ reservedWays
        -Map~RequirementRef, ClassID~ reservedClasses
        +CPUAvailable(idx, ref) bool
        +ReserveCPUs(ref, cpus) error
        +FreeWays(cacheID, ref) WayIntervals
        +ReserveWays(ref, cacheID, iv) error
        +ReserveClass(ref) ClassID
    }

    class CacheCapacity {
        +Map~string, int64~ Ways
        +ClassPool Classes
    }

    class ClassPool {
        +int NumCLOS
        +int Reserved
    }

    class ClassNamer {
        <<interface>>
        +Name(ref, taken) ClassID
    }

    class PQoSClassNamer {
        +lowestFreeCOSIndexAsText()
    }

    class RDTClassNamer {
        +deterministicControlGroupName()
    }

    AllocationLedger o-- AllocationSnapshot
    AllocationLedger o-- CacheCapacity
    AllocationLedger o-- ClassNamer
    CacheCapacity *-- ClassPool
    ClassNamer <|.. PQoSClassNamer
    ClassNamer <|.. RDTClassNamer
```

One ledger exists per deployment reconcile. `ClassPool` comes from the topology artifact (R1.45).
`ReserveClass` is the only caller of `ClassNamer.Name`. Nothing else reaches the namer, and no
implementation of it carries a capacity limit - which is why `ErrCapacityExhausted` can only come
from the ledger.

### Ownership predicate (§3.1 table)

One rule, applied identically to CPU indices and cache ways (R1.48 part 1).

```mermaid
flowchart TD
    Q[Is this CPU / way available to ref?] --> A{Held at all?}
    A -- no --> FREE[Available]
    A -- yes --> B{Reserved earlier<br/>in this pass?}
    B -- yes --> BLOCK[Blocked]
    B -- no --> C{Same deployment?}
    C -- no --> BLOCK
    C -- yes --> D{Same component<br/>RequirementRef?}
    D -- no --> BLOCK
    D -- yes --> REUSE[Reusable - retry gets its own back]
```

## 3. Capabilities and Runtime Bundle (§2.1, §5-§8)

One variation point - the runtime. The factory is the only place a CPU planner is paired with an
isolation controller, so an invalid combination cannot be constructed.

```mermaid
classDiagram
    direction TB

    class Runtime {
        +string Name
        +CPUPlanner CPUPlanner
        +CachePlanner Cache
        +ClassNamer Classes
        +CacheIsolationController Isolation
    }

    class RuntimeFactory {
        <<abstract factory>>
        +NewComposeRuntime() Runtime
        +NewKubeRuntime() Runtime
    }

    class CPUPlanner {
        <<interface>>
        +PlanCPU(req) CPUPlan
    }
    class TopologyCPUPlanner {
        +directPinningFromTopology()
    }
    class BalloonCPUPlanner {
        +nriBalloonSelection()
    }

    class CachePlanner {
        <<interface>>
        +PlanCache(req) CachePlan
    }
    class L3CachePlanner {
        +bestFitByFreeRunLength()
        +tieBreakOnLowestCacheID()
        +reserveClassOntoCachePlan()
    }

    class CacheIsolationController {
        <<interface>>
        +Apply(ctx, reservation) error
        +Verify(ctx, reservation) error
        +Release(ctx, reservation) error
    }
    class PQoSCacheController {
        +ownsCOSSpelling()
        +ownsPqosDefaultCOS()
    }
    class RDTPolicyController {
        +partitionsAndClasses()
        +waitForInformer()
    }

    RuntimeFactory ..> Runtime : builds
    Runtime o-- CPUPlanner
    Runtime o-- CachePlanner
    Runtime o-- ClassNamer
    Runtime o-- CacheIsolationController

    CPUPlanner <|.. TopologyCPUPlanner
    CPUPlanner <|.. BalloonCPUPlanner
    CachePlanner <|.. L3CachePlanner
    CacheIsolationController <|.. PQoSCacheController
    CacheIsolationController <|.. RDTPolicyController
```

| Capability | Compose runtime | Kube runtime |
|---|---|---|
| CPU planning | `TopologyCPUPlanner` | `BalloonCPUPlanner` |
| Cache planning | `L3CachePlanner` | `L3CachePlanner` |
| Class naming | `PQoSClassNamer` | `RDTClassNamer` |
| Cache isolation | `PQoSCacheController` | `RDTPolicyController` |
| Deployment config | `ComposeConfigurator` | `HelmConfigurator` |

## 4. Coordinator, Ports and Configurators (§7, §9, §10)

`ResourceCoordinator` is non-generic (R1.2). Configurators are **not** in the bundle and share no
interface - their inputs and outputs differ, and only Compose produces an artifact.

```mermaid
classDiagram
    direction LR

    class DeploymentManager {
        +deployComposeComponent() error
        +deployHelmComponent() error
        +downloadPackage()
        +chooseInstallOrUpdate()
        +reportDeploymentState()
    }

    class ResourceCoordinator {
        <<facade>>
        -Runtime runtime
        -ReservationStore store
        -CacheCapacity caps
        +Plan(ctx, request) ResourcePlan
        +Commit(ctx, plan) error
        +Activate(ctx, owner) error
        +Release(ctx, owner) error
        -newLedger(deploymentID) AllocationLedger
    }

    class ComposeConfigurator {
        +Apply(plan, sourcePath) PreparedPathAndCleanup
        +bindPlanToSingleService()
    }

    class HelmConfigurator {
        +Apply(plan, values) Values
        +balloonAnnotationFromCPUPlacement()
        +rdtAnnotationFromCachePlanClass()
    }

    class ReservationStore {
        <<interface>>
        +Snapshot() AllocationSnapshot
        +LoadReservation(owner) Reservation
        +ListOwners(deploymentID) OwnerRefs
        +SaveAllocations(deploymentID, allocations) error
        +ClearComponent(owner) error
    }

    class reservationStore {
        <<adapter>>
        -DatabaseIfc db
        +mapCacheAssignmentToDomain()
    }

    class DatabaseIfc {
        +SetAllocations(id, cpus, caches) error
    }

    class CommandRunner {
        <<interface>>
        +Run(ctx, name, args) Output
    }
    class nsenterRunner {
        +runInHostNamespace()
    }
    class directRunner {
        +runDirectly()
    }

    class RDTPolicyStore {
        <<interface>>
        +Apply(ctx, update) error
        +Remove(ctx, removal) error
    }

    class BalloonPolicyReader {
        <<interface>>
        +CurrentPolicy() ParsedBalloonPolicy
    }

    class AllocationLedger
    class PQoSCacheController
    class RDTPolicyController
    class BalloonCPUPlanner

    DeploymentManager --> ResourceCoordinator
    DeploymentManager --> ComposeConfigurator
    DeploymentManager --> HelmConfigurator
    ResourceCoordinator o-- Runtime
    ResourceCoordinator o-- ReservationStore
    ResourceCoordinator ..> AllocationLedger : owns per reconcile
    ReservationStore <|.. reservationStore
    reservationStore --> DatabaseIfc
    PQoSCacheController --> CommandRunner
    CommandRunner <|.. nsenterRunner
    CommandRunner <|.. directRunner
    RDTPolicyController --> RDTPolicyStore
    BalloonCPUPlanner --> BalloonPolicyReader
```

`DatabaseIfc.SetAllocations` is one lock acquisition, one notification, one queued persist (§9.1).
`DeploymentManager` holds no masks, balloons, YAML nodes, or PQoS knowledge.

## 5. Sequence - Compose deploy, happy path (§10)

Note the two orderings the plan makes invariant: `Commit` persists **before** it applies isolation,
and isolation is in force **before** the workload starts (§8, R1.42).

```mermaid
sequenceDiagram
    autonumber
    participant DM as DeploymentManager
    participant RC as ResourceCoordinator
    participant ST as ReservationStore
    participant LG as AllocationLedger
    participant NM as PQoSClassNamer
    participant CP as TopologyCPUPlanner
    participant KP as L3CachePlanner
    participant ISO as PQoSCacheController
    participant CFG as ComposeConfigurator
    participant WL as ComposeClient

    DM->>RC: newLedger(deploymentID)
    RC->>ST: Snapshot()
    ST-->>RC: AllocationSnapshot
    RC->>LG: NewAllocationLedger(snapshot, self, caps, classes)

    loop per component
        DM->>RC: Plan(ctx, ResourceRequest)
        RC->>RC: normalize requirements (§4)
        RC->>CP: PlanCPU(reqs, ledger)
        CP->>LG: CPUAvailable / ReserveCPUs
        CP-->>RC: CPUPlan
        RC->>KP: PlanCache(reqs, cpuPlan, ledger)
        KP->>LG: FreeWays(cacheID, ref) / ReserveWays
        KP->>LG: ReserveClass(ref)
        LG->>NM: Name(ref, taken)
        NM-->>LG: ClassID
        Note over LG,NM: the ledger grants the slot and caps the pool,<br/>the namer only spells it
        LG-->>KP: ClassID
        KP-->>RC: CachePlan with Class
        RC-->>DM: ResourcePlan

        DM->>RC: Commit(ctx, plan)
        RC->>ST: SaveAllocations(deploymentID, allocations)
        Note over RC,ST: one write per change (§9.1)
        RC->>ISO: Apply(ctx, plan.Reservation())
        ISO-->>RC: ok
        RC-->>DM: ok

        DM->>CFG: Apply(plan, composeFile)
        CFG-->>DM: preparedPath, cleanup
        DM->>WL: DeployCompose(projectName, preparedPath, env)
        Note over ISO,WL: isolation already in force before start
        WL-->>DM: ok

        DM->>RC: Activate(ctx, plan.Owner)
        RC->>ST: LoadReservation(owner)
        RC->>ISO: Verify(ctx, reservation)
        ISO-->>RC: matches
        RC-->>DM: ok
        DM->>CFG: cleanup()
    end
```

## 6. Sequence - Helm deploy (§10)

Same lifecycle, different strategies; the only structural difference is that the configurator
returns values rather than a file, so there is no cleanup.

```mermaid
sequenceDiagram
    autonumber
    participant DM as DeploymentManager
    participant RC as ResourceCoordinator
    participant BP as BalloonCPUPlanner
    participant KP as L3CachePlanner
    participant LG as AllocationLedger
    participant ISO as RDTPolicyController
    participant CFG as HelmConfigurator
    participant WL as HelmClient

    DM->>RC: Plan(ctx, req)
    RC->>BP: PlanCPU(reqs, ledger)
    BP->>BP: find compatible unoccupied balloon
    BP-->>RC: CPUPlan with Placement.Class set to the balloon name
    RC->>KP: PlanCache(reqs, cpuPlan, ledger)
    KP->>LG: ReserveWays / ReserveClass
    Note over LG: ReserveClass delegates spelling to RDTClassNamer,<br/>ErrCapacityExhausted surfaces here during Plan
    KP-->>RC: CachePlan{Class}
    RC-->>DM: ResourcePlan

    DM->>RC: Commit(ctx, plan)
    RC->>RC: SaveAllocations, then Apply
    RC->>ISO: Apply(ctx, reservation)
    ISO->>ISO: create partition + class, wait for informer
    ISO-->>RC: ok
    RC-->>DM: ok

    DM->>CFG: Apply(plan, values)
    Note over CFG: reads CachePlan.Class from the plan, not from Apply()
    CFG-->>DM: prepared values
    DM->>WL: InstallChart(release, repo, revision, values)
    WL-->>DM: ok
    DM->>RC: Activate(ctx, plan.Owner)
    RC->>ISO: Verify(ctx, reservation)
```

## 7. Sequence - Rollback on failure (§10.1, §11)

One `defer` per component, a named `err` return, and a detached context so a cancelled deploy still
gets rolled back. Release reads back what actually landed rather than releasing the in-memory plan.

```mermaid
sequenceDiagram
    autonumber
    participant DM as deployComposeComponent
    participant RC as ResourceCoordinator
    participant ST as ReservationStore
    participant ISO as CacheIsolationController
    participant WL as Workload client

    DM->>RC: Commit(ctx, plan)
    RC-->>DM: ok
    Note over DM: defer releaseOnFailure(owner) armed

    DM->>WL: Deploy / InstallChart
    WL-->>DM: error

    DM->>DM: releaseOnFailure - WithoutCancel + 30s budget
    DM->>RC: Release(releaseCtx, owner)
    RC->>ST: LoadReservation(owner)
    alt not recorded
        ST-->>RC: not found
        RC-->>DM: nil (idempotent)
    else recorded
        ST-->>RC: Reservation
        RC->>ISO: Release(ctx, reservation)
        ISO-->>RC: error or ok
        RC->>ST: ClearComponent(owner)
        Note over RC,ST: record cleared even if runtime reset failed (§11)
        RC-->>DM: errors.Join(...)
    end
    Note over DM: Release failure is logged, the original error is returned
```

## 8. Sequence - Removal and post-restart repair (§8, §11)

Both paths run without any in-memory plan or handle - the whole point of R1.49.

```mermaid
sequenceDiagram
    autonumber
    participant DM as DeploymentManager
    participant RC as ResourceCoordinator
    participant ST as ReservationStore
    participant ISO as CacheIsolationController

    rect rgb(245,245,245)
    Note over DM,ISO: Removal - enumerate from the record, not the manifest
    DM->>ST: ListOwners(deploymentID)
    ST-->>DM: OwnerRef[]
    loop per owner
        DM->>RC: Release(ctx, owner)
        RC->>ST: LoadReservation(owner)
        RC->>ISO: Release(ctx, reservation)
        RC->>ST: ClearComponent(owner)
    end
    end

    rect rgb(245,245,245)
    Note over DM,ISO: After reboot - record survived, device state did not
    DM->>RC: Activate(ctx, owner)
    RC->>ST: LoadReservation(owner)
    ST-->>RC: Reservation
    RC->>ISO: Verify(ctx, reservation)
    ISO-->>RC: absent
    RC->>ISO: Apply(ctx, reservation)
    ISO-->>RC: converged
    end
```

## 9. Lifecycle states (§8, §11)

```mermaid
stateDiagram-v2
    [*] --> Planned : Plan - pure, ledger only
    Planned --> Committed : Commit - SaveAllocations then Isolation.Apply
    Committed --> Started : configurator Apply + workload start
    Started --> Active : Activate - Isolation.Verify reports match
    Active --> Active : reconcile every 30s
    Active --> Committed : Verify reports absent, re-Apply after restart

    Planned --> [*] : plan discarded, nothing persisted
    Committed --> Released : failure triggers releaseOnFailure
    Started --> Released : failure triggers releaseOnFailure
    Active --> Released : removal
    Released --> [*] : runtime reset + ClearComponent
```

## 10. What each layer may not do (§15 guardrails)

```mermaid
flowchart LR
    subgraph Pure["Pure - no I/O of any kind"]
        N[Requirement normalization]
        L[AllocationLedger]
        CPUP[CPU planners]
        CACHEP[L3CachePlanner]
        M[Model + mask math]
    end

    subgraph Ports["Ports - side effects only here"]
        RS[ReservationStore]
        CR[CommandRunner]
        RPS[RDTPolicyStore]
        BPR[BalloonPolicyReader]
    end

    subgraph Orchestration
        RC[ResourceCoordinator]
        DM[DeploymentManager]
        CFG[Configurators]
    end

    RC --> Pure
    RC --> RS
    Ports --> Device[(Device / K8s / DB)]
    DM --> RC
    DM --> CFG
    PQOS[PQoSCacheController] --> CR
    RDT[RDTPolicyController] --> RPS
    RC --> PQOS
    RC --> RDT
```

Guardrails these diagrams are meant to make checkable:

- Nothing in **Pure** touches the database, filesystem, shell, or Kubernetes.
- `DeploymentManager` reaches the device only through `ResourceCoordinator` and the configurators.
- Only `reservationStore` imports `database`; only `*_runner.go` imports `os/exec`.
- Every scarce pool - isolated CPUs, cache ways, class slots - has exactly one allocator: the ledger.
  `ClassNamer` supplies spelling only and never a cap.
- Identity is constructed in exactly one place: requirement normalization (§4.1).
```
