# Hackathon 16 — Composite SDK Injection

## Goal

Replace per-language auto-instrumentation with a single composite SDK image that bundles all language agents and an injector binary. The operator injects this image via init container and the injector handles language detection and agent setup at runtime.

## Branch

`device-plugin-prototype` on `grafana/opentelemetry-operator`

## Key files

| File | Purpose |
|------|---------|
| `apis/v2alpha1/instrumentation_types.go` | CRD Go types — edit this to add fields |
| `apis/v2alpha1/groupversion_info.go` | API group/version registration |
| `apis/v2alpha1/zz_generated.deepcopy.go` | Auto-generated — do not edit |
| `config/crd/bases/instrumentation.opentelemetry.io_instrumentations.yaml` | Generated CRD YAML — do not edit |
| `instrumentation-v2alpha1-example.yaml` | Annotated reference example CR |
| `main.go` | Scheme registration |

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

## Status

### Done
- [x] CRD schema design (Jack + Claude)
- [x] `apis/v2alpha1/instrumentation_types.go` — full schema implemented
- [x] `instrumentation-v2alpha1-example.yaml` — annotated reference example

### TODO
- [ ] Webhook / pod mutator — core injection logic
  - CR priority resolution (multi-CR tiebreaking)
  - Per-container rule matching (namespace ∧ pod labels ∧ container names)
  - Env var injection per container
  - Declarative config ConfigMap creation + volume mount
  - Device resource request injection (or init container)
- [ ] Controller / reconciler — update `internal/deviceplugin/reconciler.go` to watch v2alpha1
- [ ] Status subresource — surface matched rule name, conflict warnings
- [ ] Validation webhook — validate `file_format`, catch invalid declarative config early
