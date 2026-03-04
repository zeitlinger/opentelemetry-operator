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

**Per-language image overrides:** `spec.injector.java/nodejs/python/dotnet` allow overriding individual language agent images independently of the composite image.

**Config mechanisms:** Two mechanisms per rule (use one or the other):
1. **Env vars** — `corev1.EnvVar` slice (supports `valueFrom.secretKeyRef` etc.). The standard approach when no declarative config is needed.
2. **Declarative config** — inline OTel declarative config (file_format: "1.0"). Operator mounts as ConfigMap, sets `OTEL_CONFIG_FILE`. **Important: SDKs ignore `OTEL_*` env vars when a config file is present**, so all SDK configuration must live in the config document. Env vars can still be set alongside declarativeConfig for non-SDK purposes (e.g. injector vars, secrets injected via `valueFrom` and referenced as `${ENV_VAR}` in the config).

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
- **TLS / volume mounts** — no mechanism to mount cert files (e.g. mTLS certs for collector communication) into instrumented containers; workaround is user-managed volumes. Separate from image volumes (which replace the init container pattern for agent binaries)
- **Annotation-based opt-out** — `config.disabled: true` on a rule handles the common case; pod-level annotation opt-out can be added later if needed

## Image Volumes — Local Testing

Image volumes replace the init container + emptyDir approach for agent injection. They require:

- **Kubernetes 1.31+** — when the `ImageVolume` feature gate was introduced (alpha)
- **containerd 2.1+** — required by the kubelet to actually mount image volumes
- **`ImageVolume` feature gate** — must be enabled on the API server and kubelet

The operator detects Kubernetes >= 1.31 and automatically uses image volumes when available, falling back to init containers on older clusters.

> **Note for kind:** `kindest/node:v1.31.x` ships with containerd 1.7.x which predates image volume support. Use `kindest/node:v1.32.x` or later, which bundles containerd 2.1.

### Create the kind cluster

Uses `kindest/node:v1.32.5` since that is the earliest kind node image that bundles containerd 2.1.

```bash
cat <<EOF | kind create cluster --name otel-operator-dev --config=-
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
  image: kindest/node:v1.32.5
  kubeadmConfigPatches:
  - |
    kind: ClusterConfiguration
    apiServer:
      extraArgs:
        feature-gates: "ImageVolume=true"
    scheduler:
      extraArgs:
        feature-gates: "ImageVolume=true"
    controllerManager:
      extraArgs:
        feature-gates: "ImageVolume=true"
  - |
    kind: KubeletConfiguration
    featureGates:
      ImageVolume: true
EOF
```

### Install cert-manager

```bash
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.19.4/cert-manager.yaml
kubectl wait --for=condition=Available deployments/cert-manager -n cert-manager --timeout=120s
```

### Build and deploy the operator

```bash
export IMG=opentelemetry-operator:$(git rev-parse --short HEAD)
make container
kind load docker-image $IMG --name otel-operator-dev
IMG=$IMG make deploy
kubectl rollout status deployment/opentelemetry-operator-controller-manager \
  -n opentelemetry-operator-system --timeout=120s
```

### Deploy the OpenTelemetry Collector

Deploy a collector that receives OTLP over HTTP and logs telemetry to stdout for easy verification:

```bash
kubectl apply -f - <<EOF
apiVersion: opentelemetry.io/v1beta1
kind: OpenTelemetryCollector
metadata:
  name: otelcol
spec:
  config:
    receivers:
      otlp:
        protocols:
          grpc:
            endpoint: 0.0.0.0:4317
          http:
            endpoint: 0.0.0.0:4318
    exporters:
      debug:
        verbosity: detailed
    service:
      pipelines:
        traces:
          receivers: [otlp]
          exporters: [debug]
        metrics:
          receivers: [otlp]
          exporters: [debug]
        logs:
          receivers: [otlp]
          exporters: [debug]
EOF
kubectl wait --for=condition=Available deployment/otelcol-collector --timeout=120s
```

### Create the Instrumentation CR

```bash
kubectl apply -f - <<EOF
apiVersion: opentelemetry.io/v1alpha1
kind: Instrumentation
metadata:
  name: my-instrumentation
spec:
  python:
    image: ghcr.io/open-telemetry/opentelemetry-operator/autoinstrumentation-python:latest
  exporter:
    endpoint: http://otelcol-collector:4318
EOF
```

### Test Python

```bash
kubectl apply -f - <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: test-python-app
  annotations:
    instrumentation.opentelemetry.io/inject-python: "true"
spec:
  containers:
  - name: app
    image: python:3.11-slim
    command: ["sleep", "infinity"]
EOF
kubectl wait --for=condition=Ready pod/test-python-app --timeout=120s
```

Verify the image volume was injected (no init container, volume type is `image`):

```bash
kubectl get pod test-python-app -o json | python3 -c "
import json, sys
pod = json.load(sys.stdin)
print('=== Volumes ===')
for v in pod['spec']['volumes']:
    print(' ', v['name'], '->', list(v.keys()))
print()
print('=== Init containers ===')
for c in pod['spec'].get('initContainers', []):
    print(' ', c['name'])
print('  (none)' if not pod['spec'].get('initContainers') else '')
print()
print('=== Volume mounts ===')
for m in pod['spec']['containers'][0]['volumeMounts']:
    print(' ', m['mountPath'], '<-', m['name'])
"
```

Expected output:
```
=== Volumes ===
  kube-api-access-xxxxx -> ['name', 'projected']
  opentelemetry-auto-instrumentation-python -> ['image', 'name']

=== Init containers ===
  (none)

=== Volume mounts ===
  /var/run/secrets/kubernetes.io/serviceaccount <- kube-api-access-xxxxx
  /otel-auto-instrumentation-python <- opentelemetry-auto-instrumentation-python
```

Verify the agent is reachable and `PYTHONPATH` points into the `/autoinstrumentation` subdirectory
(image volumes mount the full image filesystem, so agent files are one level deeper than with init containers):

```bash
# sitecustomize.py bootstraps the SDK — if this prints a path, injection is working
kubectl exec test-python-app -- python3 -c "import sitecustomize; print(sitecustomize.__file__)"

# PYTHONPATH should include /autoinstrumentation/ in the paths
kubectl exec test-python-app -- env | grep PYTHONPATH
```

Expected:
```
/otel-auto-instrumentation-python/autoinstrumentation/opentelemetry/instrumentation/auto_instrumentation/sitecustomize.py
PYTHONPATH=/otel-auto-instrumentation-python/autoinstrumentation/opentelemetry/instrumentation/auto_instrumentation:/otel-auto-instrumentation-python/autoinstrumentation
```

Send a trace and verify it appears in the collector logs:

```bash
# Watch collector in one terminal
kubectl logs deployment/otelcol-collector -f

# Trigger HTTP activity in another terminal (auto-instrumented by the SDK)
kubectl exec test-python-app -- python3 -c "
import urllib.request
urllib.request.urlopen('http://example.com')
print('done')
"
```

You should see a trace with spans for the outgoing HTTP request appear in the collector logs.

### Cleanup

```bash
kind delete cluster --name otel-operator-dev
```

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
- [x] Declarative config — reconciler creates ConfigMaps per rule, webhook mounts volume + sets `OTEL_CONFIG_FILE`
- [x] Controller / reconciler — watches Instrumentation CRs + namespaces, manages ConfigMap lifecycle with finalizer and pruning

### TODO

- [ ] **Per-language image override init containers** — see task details below
- [ ] **Status subresource** — add `InstrumentationStatus` with conditions and rule match info
- [ ] **Validation webhook** — see task details below
- [ ] **Image volumes** — separate work package (Johanna), K8s 1.31+ image volumes replace init container + emptyDir

## Task details

### Per-language image override init containers

When `spec.injector.java` (etc.) is set, add a dedicated init container per language that copies the language-specific agent from that image, overriding what the composite image provided.

**Implementation:**
1. In `inject.go`, after the composite init container, iterate over `inst.Spec.Injector.{Java,NodeJS,Python,DotNet}`
2. For each non-nil override, add an init container:
   - Name: `otel-injector-{language}` (e.g. `otel-injector-java`)
   - Image: the override image
   - Command: `cp -r /autoinstrumentation/. /otel/` (same as composite — overwrites the language-specific files)
   - VolumeMount: same `otel-injector` emptyDir at `/otel`
3. Language init containers run after the composite init container (append order), so they overwrite

**Open question:** Does the injector binary expect agents at fixed paths in `/otel/`? If so, each language image just needs to place files at the right paths. If paths are configurable via `otelinject.conf`, the override images need to match. Check the composite image layout.

**Files:** `internal/injector/inject.go`, `internal/injector/inject_test.go`

### Validation webhook

Reject invalid CRs at admission time. Follow the pattern in `apis/v1alpha1/instrumentation_webhook.go`.

**Validations:**
1. **Empty injector image** — `spec.injector.image` must be non-empty (CR is useless without it)
2. **Duplicate rule names** — rule names must be unique within a CR (colliding ConfigMap names otherwise). Empty names are fine (no ConfigMap created unless declarativeConfig is set)
3. **Reserved env var prefix** — `OTEL_INJECTOR_*` in `config.env` must be rejected (already validated at injection time in `inject.go:validateRuleEnv`, but better to catch at CR creation)
4. **Rule name DNS compatibility** — when `declarativeConfig` is set, the rule name becomes part of ConfigMap name `otel-injector-{cr}-{rule}`, so it must be lowercase alphanumeric + hyphens, max ~200 chars
5. **DeclarativeConfig requires rule name** — if a rule has `declarativeConfig` but no `name`, the ConfigMap name is indeterminate

**Files:**
- `apis/v2alpha1/instrumentation_webhook.go` — new, implement `ValidateCreate`/`ValidateUpdate`/`ValidateDelete`
- `apis/v2alpha1/instrumentation_webhook_test.go` — new
- `main.go` — register with `otelv2alpha1.SetupInstrumentationWebhook(mgr, cfg)`

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
