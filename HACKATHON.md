# Hackathon 16 — Composite SDK Injection

## Goal

Replace per-language auto-instrumentation with a single composite SDK image that bundles all language agents and an injector binary. The operator injects this image via init container and the injector handles language detection and agent setup at runtime.

## Branch

`hackathon-16-composite-sdk-injection` on `grafana/opentelemetry-operator`

## Key files

| File | Purpose |
|------|---------|
| `apis/v2alpha1/instrumentation_types.go` | CRD Go types — edit this to add fields |
| `apis/v2alpha1/groupversion_info.go` | API group/version registration |
| `apis/v2alpha1/zz_generated.deepcopy.go` | Auto-generated — do not edit |
| `config/crd/bases/instrumentation.opentelemetry.io_instrumentations.yaml` | Generated CRD YAML — do not edit |
| `instrumentation-v2alpha1-example.yaml` | Annotated reference example CR |
| `internal/injector/podmutator.go` | CR lookup + PodMutator implementation |
| `internal/injector/inject.go` | Pod mutation logic (init container, env vars) |
| `internal/injector/inject_test.go` | Unit tests |
| `main.go` | Scheme + mutator registration |

## After changing CRD types

```bash
make generate   # regenerates zz_generated.deepcopy.go
make manifests  # regenerates CRD YAML
```

Use [kubebuilder markers](https://book.kubebuilder.io/reference/markers) for validation on new fields.

## Schema design (complete)

See `instrumentation-v2alpha1-example.yaml` for a fully annotated working example.

### Key decisions

**Scope:** Cluster-scoped CR. Namespace is a selector dimension, not a CR scope.

**Multiple CRs:** Supported. Each pod/container is evaluated against all CRs. Highest `priority` field wins; creation timestamp breaks ties. Emit warning on conflict. No merge semantics.

**Rules:** Array evaluated per `(pod, container)` pair, first-match wins within the winning CR. No match = no instrumentation (safe default).

**No language-specific selection:** Language detection is runtime (injector). If you need different config per language in a multi-language pod, use `selector.containerNames` to target specific containers with separate rules.

**Image config is top-level (not per-rule):** Agent versions are a platform concern, not a policy concern. Use separate CRs with different priorities for environment-specific agent versions.

**Per-language image overrides:** `spec.injector.java/nodejs/python/dotnet` allow overriding individual language agent images independently of the composite image.

**Config mechanisms:** Two optional, composable mechanisms per rule:
1. **Env vars** — `corev1.EnvVar` slice (supports `valueFrom.secretKeyRef` etc.)
2. **Declarative config** — inline OTel declarative config (file_format: "1.0"). Operator mounts as ConfigMap, sets `OTEL_CONFIG_FILE`. SDKs ignore `OTEL_*` env vars when a config file is present. Use `${ENV_VAR}` substitution to bridge secrets from env into the config.

**Opt-out:** Set `config.disabled: true` on a rule with a tight selector, placed before any catch-all rule.

### Schema shape

```
InstrumentationSpec
  priority                int
  injector
    image                 string           # composite image (all agents + injector)
    java/nodejs/python/dotnet              # per-language image overrides (optional)
      image               string
  rules                   []Rule
    name                  string           # optional, for debuggability
    selector
      namespaces          []string         # empty = all namespaces
      podLabels           map[string]string  # AND semantics, empty = all pods
      containerNames      []string         # empty = all containers
    config
      disabled            bool             # true = suppress instrumentation
      env                 []corev1.EnvVar
      declarativeConfig   *DeclarativeConfig  # inline OTel declarative config
```

### Known gaps (future work)

- **Namespace label selectors** — currently only exact namespace name matching; selecting namespaces by label (like NetworkPolicy's `namespaceSelector`) is a v2 enhancement
- **TLS / volume mounts** — no mechanism to mount cert files for mTLS; workaround is user-managed volumes
- **Annotation-based opt-out** — `config.disabled: true` on a rule handles the common case; pod-level annotation opt-out can be added later if needed

## Injector webhook (PodMutator)

Annotation-driven injection via `instrumentation.opentelemetry.io/inject-injector`. Uses the same pattern as v1alpha1 (pod > namespace precedence, `"true"` / `"false"` / `"name"` / `"ns/name"`).

On injection, adds:
- Init container (`otel-injector-init`) that copies composite SDK contents to an emptyDir
- `LD_PRELOAD=/otel/libotelinject.so` on all app containers
- OTLP endpoint, sampler, propagator, and resource attribute env vars from the CRD spec
- Kubernetes metadata via downward API (`namespace`, `pod name`, `pod UID`)
- Service name derived from owner references (Deployment/StatefulSet/DaemonSet/Job)

## Otelinject settings mapping

The otelinject binary (via LD_PRELOAD) reads env vars and a config file. Here's how every setting maps to the CRD.

### Auto-set by operator (no user config needed)

| Setting | How |
|---|---|
| `LD_PRELOAD` | Hardcoded `/otel/libotelinject.so` |
| `OTEL_INJECTOR_CONFIG_FILE` | Hardcoded `/otel/injector/otelinject.conf` |
| `OTEL_INJECTOR_K8S_NAMESPACE_NAME` | Downward API |
| `OTEL_INJECTOR_K8S_POD_NAME` | Downward API |
| `OTEL_INJECTOR_K8S_POD_UID` | Downward API |
| `OTEL_INJECTOR_K8S_CONTAINER_NAME` | Container name from pod spec |
| `OTEL_INJECTOR_SERVICE_NAME` | Derived from owner ref (Deployment/StatefulSet/etc.) |
| `OTEL_INJECTOR_SERVICE_NAMESPACE` | Pod's namespace |
| `OTEL_EXPORTER_OTLP_PROTOCOL` | Hardcoded `http/protobuf` |

### User-configured via `config.env` or `config.declarativeConfig`

All other SDK and injector settings. Users set them as env vars in rules or in declarative config. No dedicated CRD fields — this is intentional (see "Config mechanisms" above).

| Category | Example settings |
|---|---|
| SDK endpoint | `OTEL_EXPORTER_OTLP_ENDPOINT`, per-signal `_TRACES_`/`_METRICS_`/`_LOGS_` variants |
| SDK auth | `OTEL_EXPORTER_OTLP_HEADERS` (use `valueFrom.secretKeyRef`) |
| Sampling | `OTEL_TRACES_SAMPLER`, `OTEL_TRACES_SAMPLER_ARG` |
| Propagation | `OTEL_PROPAGATORS` |
| Resources | `OTEL_RESOURCE_ATTRIBUTES`, `OTEL_SERVICE_NAME` |
| SDK control | `OTEL_SDK_DISABLED`, `OTEL_TRACES_EXPORTER`, `OTEL_METRICS_EXPORTER`, `OTEL_LOGS_EXPORTER` |
| Injector control | `OTEL_INJECTOR_LOG_LEVEL`, `OTEL_INJECTOR_DISABLED` |
| Injector resources | `OTEL_INJECTOR_RESOURCE_ATTRIBUTES`, `OTEL_INJECTOR_SERVICE_VERSION` |
| Process filtering | `OTEL_INJECTOR_INCLUDE_PATHS`, `OTEL_INJECTOR_EXCLUDE_PATHS`, `_WITH_ARGUMENTS` variants |
| Runtime control | `OTEL_INJECTOR_AUTO_INSTRUMENTATION_DISABLED` (disable specific runtimes: `jvm`, `dotnet`, etc.) |
| Complex SDK config | Histogram buckets, views, aggregation — use `declarativeConfig` |

### Not wired yet in operator

| Feature | What's needed |
|---|---|
| `config.declarativeConfig` | Operator needs to create ConfigMap, mount as volume, set `OTEL_CONFIG_FILE` |
| Per-language image overrides (`injector.java`, `.nodejs`, etc.) | Operator needs to run additional init containers per language |
| Priority conflict warnings | Operator should emit warnings when multiple CRs match same pod |
| `rules[].name` in logs/status | Surface matched rule name for debugging |

### Image-level config (baked into composite image, not CRD)

Agent paths (`jvm_auto_instrumentation_agent_path`, etc.) and default process filtering are baked into the composite image's `otelinject.conf`. Overridable via env vars if needed, but typically not user-facing.

## Status

### Done
- [x] CRD schema design (Jack + Claude)
- [x] `apis/v2alpha1/instrumentation_types.go` — full schema implemented
- [x] `instrumentation-v2alpha1-example.yaml` — annotated reference example
- [x] Injector init container + LD_PRELOAD injection
- [x] Pod mutator with rules-based matching (namespace ∧ pod labels ∧ container names)
- [x] CR priority resolution (multi-CR tiebreaking)
- [x] Env var injection per container from `config.env`
- [x] Disabled rule support (opt-out)
- [x] Annotation-based triggering removed — rules selectors are the selection mechanism
- [x] `OTEL_INJECTOR_*` env var validation (hard error on reserved prefix in user config)
- [x] User env vars win over operator defaults (`appendIfNotSet` pattern)
- [x] `OTEL_NODE_IP` / `OTEL_POD_IP` downward API vars (consistent with v1alpha1)
- [x] `OTEL_INJECTOR_RESOURCE_ATTRIBUTES` with k8s metadata + service.instance.id
- [x] `OTEL_NODE_NAME` downward API for k8s.node.name
- [x] Owner ref resource attributes (k8s.replicaset.name, k8s.statefulset.name, etc.)
- [x] Precedence documentation (service name + resource attributes)

### TODO
- [ ] Declarative config ConfigMap creation + volume mount
- [ ] Per-language image override init containers
- [ ] Controller / reconciler
- [ ] Status subresource (matched rule name, conflict warnings)
- [ ] Validation webhook
- [ ] Image volumes (separate work package, currently using init container + emptyDir)

### v1alpha1 comparison — remaining gaps

Reviewed all env vars injected by v1alpha1 (`internal/instrumentation/sdk.go`) vs v2alpha1. Status:

**Intentionally different (handled by injector binary at runtime):**
- Language-specific vars (JAVA_TOOL_OPTIONS, NODE_OPTIONS, PYTHONPATH, .NET profiling) — injector binary does language detection
- `OTEL_RESOURCE_ATTRIBUTES` — injector binary builds from `OTEL_INJECTOR_*` vars at runtime
- `OTEL_SERVICE_NAME` — injector binary sets from `OTEL_INJECTOR_SERVICE_NAME`

**Intentionally different (user sets via `config.env`):**
- `OTEL_EXPORTER_OTLP_ENDPOINT` — no dedicated CRD field, user provides in env
- `OTEL_PROPAGATORS` — user provides in env (v1alpha1 has `Spec.Propagators`)
- `OTEL_TRACES_SAMPLER` / `OTEL_TRACES_SAMPLER_ARG` — user provides in env (v1alpha1 has `Spec.Sampler`)
- TLS certs (`OTEL_EXPORTER_OTLP_CERTIFICATE` etc.) — user provides in env; volume mounts for cert files not yet supported (see known gaps above)

**Resolved gaps:**
- `k8s.deployment.name` — derived from ReplicaSet name using hash-strip heuristic (same as beyla/deriveServiceName)
- `k8s.cronjob.name` — derived from Job name using hash-strip heuristic (same as beyla)

**Not porting (intentional):**
- Annotation-based resource attributes (`resource.opentelemetry.io/*`) — v1alpha1 workaround for limited config model. v2alpha1 users set these via `config.env` on rules, which is more explicit and auditable.

## Design notes: declarative config implementation

### Problem

The pod mutator runs in the webhook path — it can mutate pods but can't easily manage cluster resources like ConfigMaps. The existing collector pattern (controller creates ConfigMap, pod references it) doesn't directly apply because we don't have a reconciler yet for v2alpha1 Instrumentation. Also, the CR is cluster-scoped but ConfigMaps are namespace-scoped.

### Option A: Controller creates ConfigMaps ahead of time

A reconciler watches Instrumentation CRs and pre-creates a ConfigMap per rule that has `declarativeConfig`. The mutator references the existing ConfigMap by a deterministic name.

- ConfigMap name: `otel-injector-{instName}-{ruleName}-{contentHash[:8]}`
- Reconciler creates/updates ConfigMaps with owner references for cleanup
- Mutator computes the same deterministic name, adds volume + mount + `OTEL_CONFIG_FILE`
- **Namespace fan-out**: Reconciler needs to create a ConfigMap copy in every namespace the rule targets. This is the main complexity — requires watching namespaces and reacting to new namespaces matching selectors.

### Option B: Embed config in env var, let injector binary handle it

Serialize declarative config into an env var (e.g. `OTEL_INJECTOR_DECLARATIVE_CONFIG_BASE64`), let otelinject write it to disk at startup.

- Pro: No ConfigMap lifecycle, no namespace fan-out, no controller needed
- Con: ~1MB env var limit (shared across all vars), puts config rendering in injector binary

### Option C: Mutator creates ConfigMaps inline (hackathon scope)

The webhook handler has a `client.Client` that can write. Create the ConfigMap in the pod's namespace during mutation.

- Deterministic naming with content hash makes re-injection idempotent
- Label for identification: `app.kubernetes.io/managed-by: opentelemetry-operator`, `opentelemetry.io/instrumentation: {instName}`
- Skip cleanup for now — orphaned ConfigMaps from old configs accumulate until a proper reconciler cleans them up
- Mount as separate read-only volume (not the emptyDir used for init container)

### Recommendation

Option C for hackathon (works without a reconciler), migrate to Option A when building the controller. Option B is a fallback if we hit issues with webhook-side writes.
