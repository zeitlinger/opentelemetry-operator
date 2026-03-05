# Hackathon 16 — Composite SDK Injection

## Goal

Replace per-language auto-instrumentation with a single composite SDK image that bundles all language agents and an injector binary. The operator injects this image via init container and the injector handles language detection and agent setup at runtime.

## Branch

`hackathon-16-composite-sdk-injection` on `grafana/opentelemetry-operator`

## How operator + injector work together

The instrumentation system spans **two repos** that connect at runtime via environment variables.

### Repos and branches

| Repo | Branch | Role |
|------|--------|------|
| [`grafana/opentelemetry-operator`](https://github.com/grafana/opentelemetry-operator) | `hackathon-16-composite-sdk-injection` | Kubernetes operator — mutates pod specs at admission time |
| [`grafana/opentelemetry-injector`](https://github.com/grafana/opentelemetry-injector) | `hackathon-16-mode-support` | LD_PRELOAD injector binary — runs inside the container before `main()` |

### The injection flow

```
┌─────────────────────────────────────────────────────────────────────┐
│ Pod admission (operator webhook)                                    │
│                                                                     │
│  1. Pod created → operator webhook fires                            │
│  2. Evaluate all Instrumentation CRs (highest priority wins)        │
│  3. For each container, find first matching rule (top-to-bottom)     │
│  4. If mode=skip → do nothing for this container                    │
│  5. Otherwise, mutate the pod spec:                                 │
│     • Add image volume (injector image → /otel)                     │
│     • Add per-language image volumes (e.g. java image → /otel-java) │
│     • Set LD_PRELOAD=/otel/autoinstrumentation/libotelinject.so     │
│     • Set OTEL_INJECTOR_MODE=install|install_unless_conflict        │
│     • Set OTEL_INJECTOR_CONFIG_FILE, OTEL_INJECTOR_SERVICE_NAME,    │
│       OTEL_INJECTOR_K8S_CONTAINER_NAME, resource attributes, etc.   │
│     • If declarativeConfig: mount ConfigMap, set OTEL_CONFIG_FILE   │
│     • Set user-defined env vars from config.env                     │
└─────────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────────────┐
│ Container startup (injector binary via LD_PRELOAD)                  │
│                                                                     │
│  1. libotelinject.so runs via .init_array BEFORE main()             │
│  2. Reads OTEL_INJECTOR_MODE env var                                │
│  3. Reads OTEL_INJECTOR_CONFIG_FILE for language agent paths        │
│  4. Detects language (inspects process, env vars, file paths)       │
│  5. Based on mode:                                                  │
│     • install → force-inject agent, overwrite any existing config   │
│     • install_unless_conflict → check for existing instrumentation: │
│       - JVM: foreign -javaagent: in JAVA_TOOL_OPTIONS → back off   │
│       - .NET: foreign CORECLR_PROFILER GUID → back off              │
│       - Node.js: foreign --require in NODE_OPTIONS → back off       │
│       - Python: no reliable foreign detection → always inject       │
│  6. Sets language-specific env vars (JAVA_TOOL_OPTIONS, etc.)       │
│  7. Sets OTEL_SERVICE_NAME, OTEL_RESOURCE_ATTRIBUTES from           │
│     OTEL_INJECTOR_* vars                                            │
│  8. main() runs with instrumentation active                         │
└─────────────────────────────────────────────────────────────────────┘
```

### Key environment variables (operator → injector)

| Env var | Set by | Read by | Purpose |
|---------|--------|---------|---------|
| `LD_PRELOAD` | operator | kernel | Loads `libotelinject.so` before `main()` |
| `OTEL_INJECTOR_MODE` | operator | injector | `install`, `skip`, `install_unless_conflict` |
| `OTEL_INJECTOR_CONFIG_FILE` | operator | injector | Path to `otelinject.conf` (agent paths, settings) |
| `OTEL_INJECTOR_SERVICE_NAME` | operator | injector | Service name → injector sets `OTEL_SERVICE_NAME` |
| `OTEL_INJECTOR_K8S_CONTAINER_NAME` | operator | injector | Container name for per-container config |
| `OTEL_INJECTOR_RESOURCE_ATTRIBUTES` | operator | injector | k8s metadata → injector sets `OTEL_RESOURCE_ATTRIBUTES` |
| `OTEL_CONFIG_FILE` | operator | SDK | Declarative config file path (when `declarativeConfig` is used) |
| `JVM_AUTO_INSTRUMENTATION_AGENT_PATH` | operator | injector | Where Java agent jar lives |
| `NODEJS_AUTO_INSTRUMENTATION_AGENT_PATH` | operator | injector | Where Node.js agent lives |

### Building the injector from source

The injector repo has `Dockerfile.operator-e2e` that builds `libotelinject.so` from Zig source and packages it as a container image suitable for image volumes:

```bash
cd ~/source/opentelemetry-injector
git checkout hackathon-16-mode-support

# Build the injector image (x86_64)
docker build -f Dockerfile.operator-e2e -t injector:dev .

# Load into kind cluster
kind load docker-image injector:dev --name otel-operator-dev
```

The resulting image has this layout:
```
/autoinstrumentation/libotelinject.so          # the LD_PRELOAD binary
/autoinstrumentation/injector/otelinject.conf  # config (agent paths)
```

When mounted as an image volume at `/otel`, the container sees:
```
/otel/autoinstrumentation/libotelinject.so
/otel/autoinstrumentation/injector/otelinject.conf
```

### Testing mode conflict detection end-to-end

Current e2e tests (`tests/e2e-instrumentation/injector-mode/`) verify **pod mutation** — the operator sets the right env vars and volumes. They use `busybox:latest` as the injector image since no actual injection happens.

To test that `install` vs `install_unless_conflict` **actually behave differently at runtime**, you need:

1. **Injector built from source** (with mode support from `hackathon-16-mode-support`)
2. **A Java app with a pre-existing `-javaagent`** (e.g. Prometheus JMX exporter) — this is the "conflict"
3. **Two test cases:**
   - `mode: install_unless_conflict` + foreign `-javaagent` → injector detects conflict, backs off, `JAVA_TOOL_OPTIONS` keeps only the foreign agent
   - `mode: install` + foreign `-javaagent` → injector forces injection, `JAVA_TOOL_OPTIONS` has both agents

This requires the full build + kind cluster flow described in `local-testing-v2alpha1.md`.

## Key files

| File | Purpose |
|------|---------|
| `apis/v2alpha1/instrumentation_types.go` | CRD Go types — edit this to add fields |
| `apis/v2alpha1/groupversion_info.go` | API group/version registration |
| `apis/v2alpha1/zz_generated.deepcopy.go` | Auto-generated — do not edit |
| `config/crd/bases/instrumentation.opentelemetry.io_instrumentations.yaml` | Generated CRD YAML — do not edit |
| `instrumentation-v2alpha1-example.yaml` | Annotated reference example CR |
| `internal/injector/podmutator.go` | CR lookup + PodMutator implementation |
| `internal/injector/inject.go` | Pod mutation logic (init container, env vars, config mount) |
| `internal/injector/controller.go` | Reconciler — ConfigMap lifecycle for declarativeConfig |
| `internal/injector/inject_test.go` | Unit tests |
| `main.go` | Scheme + mutator + controller registration |

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

**Image config is top-level (not per-rule):** You want all workloads in a CR to use the same agent versions — mixing agent versions across rules within one CR creates confusion and upgrade headaches. If you need different agent versions for different environments (e.g. canary a new Java agent in staging), create a separate CR with higher priority and a namespace selector.

**Per-language agent images:** `spec.java/nodejs/python/dotnet` are optional plain strings that add a per-language init container. When set, the operator injects an env var telling the injector binary where that language's agent lives.

**Config mechanisms:** Two mechanisms per rule (use one or the other):
1. **Env vars** — `corev1.EnvVar` slice (supports `valueFrom.secretKeyRef` etc.). The standard approach when no declarative config is needed.
2. **Declarative config** — inline OTel declarative config (file_format: "1.0"). Operator mounts as ConfigMap, sets `OTEL_CONFIG_FILE`. **Important: SDKs ignore `OTEL_*` env vars when a config file is present**, so all SDK configuration must live in the config document. Env vars can still be set alongside declarativeConfig for non-SDK purposes (e.g. injector vars, secrets injected via `valueFrom` and referenced as `${ENV_VAR}` in the config).

**Conflict-aware instrumentation:** `config.mode` is a tri-state enum (`install`, `skip`, `install_unless_conflict`). Default is `install_unless_conflict` — the injector detects existing manual instrumentation (e.g. Python `sitecustomize`, Node.js SDK imports) and backs off if found. `install` forces instrumentation ("steamroll"), `skip` disables it (opt-out). This replaces the previous `disabled: bool`. Can be set at `spec.defaults.mode` (CR-wide default) and overridden per rule at `config.mode`.

### Schema shape

```
InstrumentationSpec
  priority                int
  injector                string           # injector binary image (libotelinject.so + otelinject.conf)
  java                    string           # Java agent image (optional)
  nodejs                  string           # Node.js agent image (optional)
  python                  string           # Python agent image (optional)
  dotnet                  string           # .NET agent image (optional)
  defaults                                 # CR-wide defaults (overridable per rule)
    mode                  InstrumentationMode  # install | skip | install_unless_conflict (default)
  rules                   []Rule
    name                  string           # optional, for debuggability
    selector
      namespaces          []string         # empty = all namespaces
      podLabels           map[string]string  # AND semantics, empty = all pods
      containerNames      []string         # empty = all containers
    config
      mode                InstrumentationMode  # overrides spec.defaults.mode
      env                 []corev1.EnvVar
      declarativeConfig   *DeclarativeConfig  # inline OTel declarative config
```

### Known gaps (future work)

- **Namespace label selectors** — currently only exact namespace name matching; selecting namespaces by label (like NetworkPolicy's `namespaceSelector`) is a v2 enhancement
- **TLS / volume mounts** — no mechanism to mount cert files (e.g. mTLS certs for collector communication) into instrumented containers; workaround is user-managed volumes. Separate from image volumes (which replace the init container pattern for agent binaries)
- **Annotation-based opt-out** — `config.mode: skip` on a rule handles the common case; pod-level annotation opt-out can be added later if needed

## Decisions (Mar 4 sync)

1. **Use existing composite images for hackathon** — env var overrides point the injector to the right SDK paths. Split images deferred to post-hackathon.
2. **Tri-state instrumentation mode** — `config.disabled: bool` → `config.mode: install|skip|install_unless_conflict` with `install_unless_conflict` as default. Also settable as CR-wide default at `spec.defaults.mode`.
3. **Temporary language-specific images** (Jack) — copy existing operator images with file layout adjusted to match injector expectations.
4. **Python image restructuring → injector SIG** (Nikola) — propose env var overrides per glibc flavor and standardize Python layout to match .NET.
5. **Crash-loop auto-recovery** (stretch goal) — operator watches k8s events for restart counts / failure reasons and avoids re-instrumenting failing pods.
6. **Operator internal telemetry** — export instrumentation status metrics via OTel collector, scrapeable by Prometheus for a community dashboard.

## Status

### TODO

- [ ] **Image volumes** (Johanna) — K8s 1.31+ image volumes replace init container + emptyDir. v1alpha1 support done (`internal/instrumentation/`), needs porting to v2alpha1 injector (`internal/injector/inject.go`)
- [ ] **Mode conflict detection e2e test** — build injector from source and verify conflict detection end-to-end (see "Testing mode conflict detection end-to-end" above)
- [ ] **Operator internal telemetry** — export operator metrics (instrumentation status per pod, failures) via OTel collector for external monitoring / Prometheus dashboard
- [ ] **Crash-loop auto-recovery** (Gregor, stretch) — detect instrumentation-induced pod failures and avoid re-instrumenting failing pods

### Future work (post-hackathon)

- [ ] **Node.js conflict detection** (Nikola) — upstream equivalent of Python's `sitecustomize` safety checks for Node.js
- [ ] **Python image restructuring** (Nikola → injector SIG) — standardize Python image to match .NET layout (both glibc+musl in one dir); propose env var overrides per glibc flavor to injector SIG
- [ ] **Split language images** — decompose composite image into individual shareable images (deferred from hackathon, depends on injector SIG alignment)

## v1alpha1 comparison

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

**Not porting (intentional):**
- Annotation-based resource attributes (`resource.opentelemetry.io/*`) — v1alpha1 workaround for limited config model. v2alpha1 users set these via `config.env` on rules, which is more explicit and auditable.
