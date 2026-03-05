# Crash-Loop Auto-Recovery — Detailed Design

This document contains the full research, design questions, and rationale for crash-loop auto-recovery. See [HACKATHON.md](HACKATHON.md) for the summary of key decisions.

## Problem

When the operator injects instrumentation (via `LD_PRELOAD` + init containers) and the application crashes because of it, the pod enters `CrashLoopBackOff` and stays broken until someone manually intervenes. We want the operator to detect this and automatically back off.

## How Odigos does it (the only OSS operator with this feature)

Odigos has a full automatic rollback system. Dash0 does **not** — they only support manual opt-out via labels.

**Odigos rollback flow:**

1. **Detection** — A controller watches pods matching instrumented workloads. It checks `ContainerStatus.State.Waiting.Reason` for `CrashLoopBackOff` or `ImagePullBackOff`.

2. **Decision** — Rollback triggers when ALL conditions are true:
   - Pods are in backoff state
   - Instrumentation was applied recently (within **stability window** — default **1 hour**)
   - Backoff has lasted longer than **grace period** (default **5 minutes**)
   - Agent injection is currently enabled

   The stability window is key: crashes that happen >1 hour after instrumentation are assumed unrelated. The grace period avoids reacting to transient startup blips.

3. **Rollback action:**
   - Sets `AgentInjectionEnabled = false` on their `InstrumentationConfig` CR (spec field)
   - Sets `RollbackOccurred = true` in status
   - Restarts workload (adds `kubectl.kubernetes.io/restartedAt` annotation to pod template)
   - Next pods created → webhook sees `AgentInjectionEnabled=false` → no injection

4. **Recovery** — Annotation-based state machine on the `InstrumentationConfig` CR:
   - User/frontend sets `odigos.io/rollback-recovery` annotation on the Source object
   - Controller propagates to IC, clears `RollbackOccurred`, re-enables injection
   - `odigos.io/rollback-recovery-processed` tracks idempotency

5. **Pre-existing crash protection** — Before instrumenting, checks if pods are already in `CrashLoopBackOff` (without the Odigos label). If so, skips instrumentation entirely to avoid making things worse.

**What touches workload manifests:**
- `kubectl.kubernetes.io/restartedAt` annotation on pod template (standard k8s restart trigger)
- `odigos.io/agents-meta-hash` label on pods (set by webhook, used to distinguish instrumented vs non-instrumented pods for backoff detection)
- Recovery annotations live on the `InstrumentationConfig` CR, NOT on workloads

**Configuration** (on their `OdigosConfiguration`):
- `instrumentation-auto-rollback-grace-time` (default 5 min)
- `instrumentation-auto-rollback-stability-window` (default 1 hour)
- `instrumentation-auto-rollback-disabled` (kill switch)

**Edge cases they handle:**
- Webhook-instrumented pods crashing before rollout completes (uses `AgentsMetaHashChangedTime` as fallback)
- Rate limiter bypass — rollback gets priority over queued instrumentations
- Argo Rollouts use `spec.restartAt` instead of annotation

**Key Odigos source files:**
- `instrumentor/controllers/agentenabled/rollout/rollout.go` — core logic (`shouldTriggerRollback()` line 446, `triggerRollback()` line 502)
- `instrumentor/controllers/agentenabled/rollout/rollout_backoff_helpers.go` — pod status checking
- `api/odigos/v1alpha1/instrumentationconfig_types.go` — CRD fields (`RollbackOccurred`, `InstrumentationTime`)
- `common/consts/consts.go` — default timing constants (lines 148-151)
- `instrumentor/controllers/agentenabled/pods_webhook.go` — webhook checks `AgentInjectionEnabled` before injecting (line 136)

## Design questions and decisions

### Q1: Where does rollback state live?

Odigos uses their `InstrumentationConfig` CR (spec + status fields). We have our `Instrumentation` CR. Options:
- **Option A: Status sub-resource on Instrumentation CR** — Add per-workload rollback status (e.g. `status.instrumentedWorkloads[]` with workload ref, timestamp, reason). Pro: all state in one place. Con: single CR tracks many workloads, status could get large.
- **Option B: Separate RollbackStatus CR per workload** — Pro: clean separation, easy to watch. Con: more CRDs.
- **Option C: Annotation on the workload** — Pro: simple. Con: violates our "no annotation littering" goal.

**Decision: Option A.** Our CR is already cluster-scoped and tracks multiple workloads via rules. Status is a sub-resource managed independently from spec — user `kubectl apply` on the CR spec doesn't overwrite status. Only our controller writes status.

### Q2: How do we "undo" injection?

Our injection happens at pod admission via webhook (`LD_PRELOAD`, env vars, image volumes). "Undoing" requires:
1. Mark the workload as rolled-back (so webhook knows not to inject next time)
2. Trigger a rollout restart (so new pods are created without injection)

The webhook must check rollback state before injecting. This means the podmutator needs to query rollback status during admission.

### Q3: How do we trigger the rollout restart without littering?

Three options considered:

- **Option A: Pod template annotation** (`kubectl.kubernetes.io/restartedAt`) — Patch `spec.template.metadata.annotations` with a timestamp. This is what `kubectl rollout restart` does. Changes the pod template hash → triggers a rolling update that respects `maxUnavailable`/`maxSurge`. Works for Deployments, StatefulSets, DaemonSets. The annotation persists in the pod template (visible in `kubectl get deploy -o yaml`), but it's a standard k8s convention, not "our" annotation — any `kubectl rollout restart` adds the same one.
- **Option B: Delete pods directly** — List pods owned by the workload, delete them. Zero manifest changes. But: doesn't respect rolling update strategy (risk of downtime), ordering issues with StatefulSets, and simulating a rolling restart by deleting pods one-at-a-time means reimplementing what the Deployment controller already does natively.
- **Option C: Scale down/up** — Scale replicas to 0 then back. Causes full downtime, two mutations to workload spec (arguably worse than a template annotation).

**Decision: Option A.** The annotation is standard k8s, lives in the pod template (not top-level workload metadata), and may already be present from previous `kubectl rollout restart` invocations. Fighting this to keep manifests pristine means reimplementing rolling update logic ourselves — more code, more edge cases, for marginal benefit.

### Q4: How do we identify "our" pods for backoff detection?

Odigos uses a label (`odigos.io/agents-meta-hash`) on pods. We considered three options:

- **Option A: Add a label to injected pods** — e.g. `instrumentation.opentelemetry.io/injected=true`. Minimal littering (ephemeral pods) but still touches pod manifests.
- **Option B: Check if pod has our env vars** — Look for `LD_PRELOAD` pointing to our path. Unreliable: users may set their own `LD_PRELOAD` for unrelated reasons.
- **Option C: Instrumentation state DB in CR status** — The controller watches pod events, sees new pods for workloads matching a rule, and records state in `status.instrumentedWorkloads[]`. No labels, no env var inspection — identification comes from our own records.

**Decision: Option C.** Zero pod/workload manifest changes. The controller already needs to watch pods for crash detection — recording "we instrumented workload X at time T" is an additional status write in the same reconciliation loop. No admission webhook latency impact (webhook just mutates the pod as today; the controller writes status asynchronously). The gap between pod creation and status write (seconds) doesn't matter — `CrashLoopBackOff` takes at least one restart + backoff delay to appear.

This also gives us a useful **instrumentation inventory** for free: operators can see exactly which workloads are instrumented, by which CR/rule, and since when.

### Q5: What timing defaults, and where is it configurable?

Odigos uses 5-minute grace + 1-hour stability window, configurable globally (not per-workload).

**Grace period (default 5m):** How long to tolerate `CrashLoopBackOff` before rolling back. Most apps start in <1 minute. JVM apps with large classpaths might take 2-3 minutes. 5 minutes covers the vast majority. Apps that legitimately take >5 minutes to start are rare and usually have `initialDelaySeconds` on their probes already — those don't show `CrashLoopBackOff` because kubelet waits. Also note that Kubernetes itself applies exponential backoff (10s, 20s, 40s, 80s, ..., capped at 5min), so by the time 5 minutes of backoff has elapsed, the pod has crashed and restarted multiple times — this is not a false positive.

**Stability window (default 1h):** After instrumentation, how long to blame crashes on us. Beyond this, crashes are assumed unrelated (app bug, OOM, config change). 1 hour is conservative — most instrumentation-caused crashes happen within seconds/minutes of startup (class conflicts, missing dependencies, agent startup failures). The risk of too long: rolling back for an unrelated crash. The risk of too short: missing a delayed issue like an agent-induced memory leak. 1 hour balances toward fewer false positives.

**Decision: CR-wide only (`spec.defaults.rollback`).** One grace period and stability window for all workloads in the CR. Rationale:
- The 5m/1h defaults work for nearly all workloads — the cases where they don't (very slow startup) are rare and those apps typically have `initialDelaySeconds` which prevents `CrashLoopBackOff` from appearing in the first place.
- If a workload genuinely needs different timing, the user can create a separate `Instrumentation` CR with higher priority targeting just that workload — we already support this pattern for `mode`.
- This matches how Odigos does it (global config only) and how we handle other CR-wide defaults.
- Per-rule overrides can be added later as a backwards-compatible schema addition if real demand appears. Starting without them avoids premature schema complexity.

### Q6: How does recovery work?

After rollback, the workload stays uninstrumented until something changes. Options:
- **Automatic retry on CR change** — If the `Instrumentation` CR is updated (e.g. new agent image), clear rollback state and retry. This is the most natural trigger ("operator deployed a fix").
- **Manual recovery** — User deletes a status entry or sets a field. Simple but requires manual intervention.
- **Time-based retry** — Retry after N hours. Risk of crash-loop recurrence.

**Decision: Automatic retry on CR spec change + manual override. No time-based retry.** The controller tracks `crGeneration` in each inventory entry. When `CR.metadata.generation > entry.crGeneration`, the rollback info is cleared and the webhook injects normally on the next pod creation.

### Q7: Pre-existing crash protection?

Should we skip injection for workloads already in `CrashLoopBackOff`? Odigos does this.

**Decision: Yes.** Don't make a bad situation worse. Before first injection, controller checks if workload's pods are already in `CrashLoopBackOff`. If so, skips injection and logs a warning event. Could be deferred to v2 if needed.
