# Crash-loop Auto-recovery

Auto-detect when instrumentation causes a pod to crash-loop and automatically back off. Full design rationale and research in [CRASHLOOP-RECOVERY-DESIGN.md](../CRASHLOOP-RECOVERY-DESIGN.md).

## Timing model

```mermaid
flowchart TD
    A[Pod crashes] --> B{Time since injection\n< stability window?\n1h default}
    B -- No --> C[Ignore — crash is\nunrelated to instrumentation]
    B -- Yes --> D{CrashLoopBackOff\nlasting ≥ grace period?\n5m default}
    D -- No, recovers --> E[No action —\ntransient startup issue]
    D -- Yes, sustained --> F[ROLLBACK]
```

- **Grace period (5m):** How long a pod must be in `CrashLoopBackOff` before we roll back. Avoids reacting to transient startup issues (e.g. waiting for a database).
- **Stability window (1h):** How long after injection we attribute crashes to instrumentation. After this, crashes are assumed unrelated (app bug, OOM, config change).

## Key decisions

| # | Question | Decision |
|---|----------|----------|
| Q1 | Where does rollback state live? | **Status sub-resource on Instrumentation CR** (`status.instrumentedWorkloads[]`) — also serves as instrumentation inventory |
| Q2 | How to undo injection? | Webhook checks rollback state before injecting; controller triggers rollout restart for existing pods |
| Q3 | How to trigger rollout restart? | **`kubectl.kubernetes.io/restartedAt` pod template annotation** — standard k8s mechanism, avoids reimplementing rolling update logic |
| Q4 | How to identify instrumented workloads? | **Instrumentation state DB in CR status** — zero pod/workload manifest changes; controller records state asynchronously, no labels or env var inspection needed |
| Q5 | Timing config scope? | **CR-wide only** (`spec.defaults.rollback`) — 5m grace, 1h stability window. Per-rule overrides deferred (backwards-compatible addition later if needed). Use separate CR with higher priority for workload-specific timing |
| Q6 | How does recovery work? | **Automatic retry on CR spec change** — rollback entry records `crGeneration`; when someone updates the CR (e.g. new agent image), generation increases, controller clears rollback and re-injects. Manual override via `kubectl patch` on status. No time-based retry (same broken agent would just crash again) |
| Q7 | Pre-existing crash protection? | **Yes** — skip injection for workloads already in `CrashLoopBackOff`, log warning |

## Flow diagrams

**Admission (webhook decides whether to inject):**

```mermaid
flowchart TD
    A[Pod created, matches rule] --> B{Workload pods already\nin CrashLoopBackOff?}
    B -- Yes --> S[Skip injection,\nlog warning]
    B -- No --> C{Workload in\nstatus.instrumentedWorkloads\nwith rollback set?}
    C -- Yes, CR unchanged --> S
    C -- No, or CR\ngeneration changed --> D[Inject: LD_PRELOAD,\nenv vars, volumes]
```

**Rollback controller (async, watches pods):**

```mermaid
flowchart TD
    A[New pod for\ninstrumented workload] --> B[Record in\nstatus.instrumentedWorkloads]
    B --> C{Pod enters\nCrashLoopBackOff?}
    C -- No --> D[Healthy — no action\nafter stability window]
    C -- Yes --> E{Within grace\nperiod? 5m}
    E -- Yes --> F[Requeue, wait]
    F --> C
    E -- No --> G[Set rollback info\non inventory entry]
    G --> H[Patch pod template\nrestartedAt annotation]
    H --> I[New pods created\nwithout injection]
```

**Recovery:**

```mermaid
flowchart TD
    A[Workload uninstrumented\nafter rollback] --> B{CR spec changed?\nnew generation}
    B -- No --> A
    B -- Yes --> C[Clear rollback info]
    C --> D[Next pod admission\n→ webhook injects normally]
```

## Schema additions

```
InstrumentationSpec
  defaults
    rollback
      enabled           bool              # default true
      graceTime         metav1.Duration   # default 5m
      stabilityWindow   metav1.Duration   # default 1h

InstrumentationStatus
  instrumentedWorkloads []InstrumentedWorkload
    workloadRef
      kind              string            # Deployment, StatefulSet, etc.
      namespace         string
      name              string
    ruleName            string            # which rule matched
    instrumentedAt      metav1.Time       # when injection was first applied
    crGeneration        int64             # CR generation at time of injection
    rollback            *RollbackInfo     # nil if not rolled back
      reason            string            # CrashLoopBackOff, ImagePullBackOff
      rolledBackAt      metav1.Time
```

## Implementation sketch

1. **New controller** (`internal/injector/rollback_controller.go`) — watches pods, maintains `status.instrumentedWorkloads[]` inventory, detects `CrashLoopBackOff`, triggers rollback after grace period
2. **Podmutator change** (`internal/injector/inject.go`) — checks rollback state before injecting; skips if rolled back and CR unchanged, re-injects if CR generation changed
3. **Pre-existing crash check** — skip injection for workloads already in `CrashLoopBackOff` (optional, could be v2)
