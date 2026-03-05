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
- **Annotation-based opt-out** — `config.mode: Skip` on a rule handles the common case; pod-level annotation opt-out can be added later if needed
- **Node.js conflict detection** — Python conflict detection exists (`sitecustomize` safety checks); Node.js equivalent needs upstream work (Nikola)

## Decisions (Mar 4 sync)

1. **Use existing composite images for hackathon** — env var overrides point the injector to the right SDK paths. Split images deferred to post-hackathon.
2. **Tri-state instrumentation mode** — `config.disabled: bool` → `config.mode: install|skip|install_unless_conflict` with `install_unless_conflict` as default. Also settable as CR-wide default at `spec.defaults.mode`.
3. **Temporary language-specific images** (Jack) — copy existing operator images with file layout adjusted to match injector expectations.
4. **Python image restructuring → injector SIG** (Nikola) — propose env var overrides per glibc flavor and standardize Python layout to match .NET.
5. **Crash-loop auto-recovery** (stretch goal) — operator watches k8s events for restart counts / failure reasons and avoids re-instrumenting failing pods.
6. **Operator internal telemetry** — export instrumentation status metrics via OTel collector, scrapeable by Prometheus for a community dashboard.

## Status

### Done
- [x] CRD schema design (Jack + Claude)
- [x] `apis/v2alpha1/instrumentation_types.go` — full schema implemented
- [x] `instrumentation-v2alpha1-example.yaml` — annotated reference example
- [x] Injector init container + LD_PRELOAD injection
- [x] Pod mutator with rules-based matching (namespace ∧ pod labels ∧ container names)
- [x] CR priority resolution (multi-CR tiebreaking)
- [x] Env var injection per container from `config.env`
- [x] Disabled rule support (opt-out) — being replaced by tri-state `config.mode`
- [x] Annotation-based triggering removed — rules selectors are the selection mechanism
- [x] `OTEL_INJECTOR_*` env var validation (hard error on reserved prefix in user config)
- [x] User env vars win over operator defaults (`appendIfNotSet` pattern)
- [x] `OTEL_NODE_IP` / `OTEL_POD_IP` downward API vars (consistent with v1alpha1)
- [x] `OTEL_INJECTOR_RESOURCE_ATTRIBUTES` with k8s metadata + service.instance.id
- [x] `OTEL_NODE_NAME` downward API for k8s.node.name
- [x] Owner ref resource attributes (k8s.replicaset.name, k8s.statefulset.name, etc.)
- [x] Precedence documentation (service name + resource attributes)
- [x] Declarative config — reconciler creates ConfigMaps per rule, webhook mounts volume + sets both `OTEL_CONFIG_FILE` and `OTEL_EXPERIMENTAL_CONFIG_FILE` (both set because SDKs haven't stabilized the env var name yet)
- [x] Controller / reconciler — watches Instrumentation CRs + namespaces, manages ConfigMap lifecycle with finalizer and pruning
- [x] Config file env vars (`OTEL_CONFIG_FILE`, `OTEL_EXPERIMENTAL_CONFIG_FILE`) blocked in rule env — operator sets them automatically
- [x] Catch-all rules skip `kube-*` system namespaces — **note:** only `kube-*` prefix is skipped automatically; other system namespaces (e.g. `opentelemetry-operator-system`, `cert-manager`) require an explicit `disabled: true` rule in the CR (see `scratch/instrumentation-v2alpha1.yaml`)
- [x] Name truncation uses hash suffix to avoid collisions (ConfigMap names at 253, volume names at 63)

### TODO

- [x] **Tri-state `config.mode`** — replaced `disabled: bool` with `InstrumentationMode` enum (`install`/`skip`/`install_unless_conflict`), added `spec.defaults.mode` with rule-level override. Injector receives resolved mode via `OTEL_INJECTOR_MODE` env var
- [x] **Temporary language-specific images** — per-language injector images (`injector-java`, `injector-nodejs`, `injector-python`, `injector-dotnet`) built and tested end-to-end (Java traces confirmed in collector)
- [x] **Per-language agent init containers** — implemented in `inject.go`; per-language init containers added when `spec.java/nodejs/python/dotnet` are set; verified working
- [x] **SDK path env var overrides** — `JVM_AUTO_INSTRUMENTATION_AGENT_PATH`, `NODEJS_AUTO_INSTRUMENTATION_AGENT_PATH`, `PYTHON_AUTO_INSTRUMENTATION_AGENT_PATH_PREFIX`, `DOTNET_AUTO_INSTRUMENTATION_AGENT_PATH_PREFIX` injected into containers when per-language images are configured
- [x] **Status subresource** — `InstrumentationStatus` with `Ready` condition (set by reconciler with rule/ConfigMap counts, error messages on failure)
- [x] **Validation webhook** — `apis/v2alpha1/instrumentation_webhook.go` validates: empty image, duplicate names, reserved env vars, DNS rule names, declarativeConfig requires name, disabled+declarativeConfig conflict
- [ ] **Image volumes** (Johanna) — K8s 1.31+ image volumes replace init container + emptyDir. v1alpha1 support done (`internal/instrumentation/`), needs porting to v2alpha1 injector (`internal/injector/inject.go`)
- [x] **Local testing with kind** — Java and Node.js traces confirmed end-to-end in collector
- [ ] **Operator internal telemetry** — export operator metrics (instrumentation status per pod, failures) via OTel collector for external monitoring / Prometheus dashboard
- [ ] **Crash-loop auto-recovery** (Gregor, stretch) — detect instrumentation-induced pod failures (restart count, failure reason from k8s events) and avoid re-instrumenting failing pods
- [x] **Declarative config e2e test** — `tests/e2e-instrumentation/injector-declarative-config/`
- [ ] **Mode conflict detection e2e test** — build injector from source (`grafana/opentelemetry-injector` branch `hackathon-16-mode-support`, `Dockerfile.operator-e2e`) and verify conflict detection end-to-end. Deploy a Java app with an existing foreign `-javaagent` (e.g. Prometheus JMX exporter): (1) `install_unless_conflict` → injector detects conflict, backs off, (2) `mode: install` → forces injection over existing agent. Requires full kind cluster with image volumes enabled (see "How operator + injector work together" section above).
- [x] **Full lifecycle e2e tests (Java + Node.js)** — `tests/e2e-instrumentation/injector-java/` and `injector-nodejs/`: deploy collector, instrument real app via v2alpha1 CR, verify telemetry (spans + metrics) arrives at collector with correct resource attributes (`service.name`, `k8s.deployment.name`, `service.instance.id`)
- [x] **Existing injector test fixes** — fixed `injector` field from object (`injector:\n  image:`) to string format in all 5 existing tests (broken since `3a25e6f2` flattened the type); added `OTEL_INJECTOR_SERVICE_NAMESPACE`, `OTEL_INJECTOR_RESOURCE_ATTRIBUTES` assertions to all existing tests; added comprehensive downward API + resource attribute verification to `injector-basic`
- [x] **Makefile injector e2e integration** — added `add-image-injector` target (sed-replaces `{{INJECTOR_IMG}}`/`{{INJECTOR_JAVA_IMG}}`/`{{INJECTOR_NODEJS_IMG}}` placeholders in test YAMLs); wired `load-image-injector-all` + `add-image-injector` into `prepare-e2e`

### Future work (post-hackathon)

- [ ] **Node.js conflict detection** (Nikola) — upstream equivalent of Python's `sitecustomize` safety checks for Node.js
- [ ] **Python image restructuring** (Nikola → injector SIG) — standardize Python image to match .NET layout (both glibc+musl in one dir); propose env var overrides per glibc flavor to injector SIG
- [ ] **Split language images** — decompose composite image into individual shareable images (deferred from hackathon, depends on injector SIG alignment)

## Task details

### Tri-state `config.mode` (replaces `disabled: bool`)

Replace the boolean `disabled` field with an `InstrumentationMode` enum:
- `install` — force instrumentation even if existing manual instrumentation is detected
- `skip` — suppress instrumentation (replaces `disabled: true`)
- `install_unless_conflict` (default) — install unless the injector detects existing instrumentation (e.g. Python `sitecustomize` conflict)

**Implementation:**
1. Add `InstrumentationMode` string type + constants to `instrumentation_types.go`
2. Add `spec.defaults.mode` field (CR-wide default)
3. Replace `config.disabled bool` with `config.mode *InstrumentationMode` on `RuleConfig`
4. In `inject.go`, resolve effective mode: rule-level overrides CR-level default, absent = `InstallUnlessConflict`
5. Pass resolved mode to injector via env var (e.g. `OTEL_INJECTOR_MODE`)
6. Update example CR, tests, validation webhook

**Files:** `apis/v2alpha1/instrumentation_types.go`, `internal/injector/inject.go`, `internal/injector/inject_test.go`

### Validation webhook

Reject invalid CRs at admission time. Follow the pattern in `apis/v1alpha1/instrumentation_webhook.go`.

**Validations:**
1. **Empty injector image** — `spec.injector` must be non-empty (CR is useless without it)
2. **Duplicate rule names** — rule names must be unique within a CR (colliding ConfigMap names otherwise). Empty names are fine (no ConfigMap created unless declarativeConfig is set)
3. **Reserved env vars** — `OTEL_INJECTOR_*`, `OTEL_CONFIG_FILE`, and `OTEL_EXPERIMENTAL_CONFIG_FILE` in `config.env` must be rejected (already validated at injection time in `inject.go:validateRuleEnv`, but better to catch at CR creation)
4. **Rule name DNS compatibility** — when `declarativeConfig` is set, the rule name becomes part of ConfigMap name `otel-injector-{cr}-{rule}`, so it must be lowercase alphanumeric + hyphens, max ~200 chars
5. **DeclarativeConfig requires rule name** — if a rule has `declarativeConfig` but no `name`, the ConfigMap name is indeterminate
6. **Disabled + declarativeConfig conflict** — `disabled: true` with `declarativeConfig` set is contradictory (config would be created but never mounted). Reject at admission.

**Files:**
- `apis/v2alpha1/instrumentation_webhook.go` — new, implement `ValidateCreate`/`ValidateUpdate`/`ValidateDelete`
- `apis/v2alpha1/instrumentation_webhook_test.go` — new
- `main.go` — register with `otelv2alpha1.SetupInstrumentationWebhook(mgr, cfg)`

### Injector-side mode implementation (`opentelemetry-injector`)

The operator passes `OTEL_INJECTOR_MODE` to the injector binary. Values are lowercase to match injector env var conventions: `install`, `skip`, `install_unless_conflict`.

**`config.zig`:**
- Add `mode` field to config struct (enum: `install`, `skip`, `install_unless_conflict`)
- Read from `OTEL_INJECTOR_MODE` env var, default `install_unless_conflict`
- Config file key: `mode`

**`root.zig`:**
- After config read + allow/deny evaluation, branch on mode:
  - `skip` → return early, log "mode=skip, skipping injection"
  - `install` → set `force` flag, pass to language modules. **Expert option** — forces injection even over existing instrumentation. Log at warn level what was overwritten.
  - `install_unless_conflict` → set `detect_conflicts` flag, pass to language modules

**Per-language conflict detection (`install_unless_conflict` mode):**

| Language | Conflict signal | Behavior |
|----------|----------------|----------|
| **JVM** (`jvm.zig`) | Any existing `-javaagent:` in `JAVA_TOOL_OPTIONS` with a *different* path | Always back off — a foreign agent is present |
| **Python** (`python.zig`) | Foreign paths in `PYTHONPATH` pointing to OTel SDK packages, or existing `sitecustomize.py` that isn't ours | Back off |
| **.NET** (`dotnet.zig`) | `CORECLR_ENABLE_PROFILING=1` already set with a different `CORECLR_PROFILER` GUID | Back off — only one profiler can run |
| **Node.js** (`nodejs.zig`) | Existing `--require` in `NODE_OPTIONS` pointing to OTel SDK | Back off (incomplete — needs upstream work, see Nikola's note) |

**`install` (force) mode:**
- Skip double-injection checks — overwrite existing values
- Log at warn level: "mode=install, forcing injection over existing {details}"

**Logging/reporting:**
- Log at info level when conflict detected and backing off
- Log at warn level when `mode=install` forces over existing instrumentation

**Reference:** Beyla's `scanner.go` detects languages via `/proc/PID/maps` (`libjvm.so`, `libcoreclr.so`, `node`, `python`) and tracks SDK versions via `BEYLA_INJECTOR_SDK_PKG_VERSION`. Different approach (process inspection vs env var checks) but useful reference for future runtime detection.

**Files:** `src/config.zig`, `src/root.zig`, `src/python.zig`, `src/jvm.zig`, `src/nodejs.zig`, `src/dotnet.zig`

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
