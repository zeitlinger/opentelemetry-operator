# Hackathon 16 — Composite SDK Injection

## Goal

Replace per-language auto-instrumentation with a single composite SDK image that bundles all language agents and an injector binary. The operator injects this image via init container and the injector handles language detection and agent setup at runtime.

## Branch

`hackathon-16-v2alpha1-placeholder-crd` on `grafana/opentelemetry-operator`

## What's here so far

### v2alpha1 Instrumentation CRD (placeholder)

A minimal `Instrumentation` CRD under a new API group:

- **Group:** `instrumentation.opentelemetry.io`
- **Version:** `v2alpha1`
- **Short name:** `instr2`

```yaml
apiVersion: instrumentation.opentelemetry.io/v2alpha1
kind: Instrumentation
metadata:
  name: my-instrumentation
spec:
  injector:
    image: "ghcr.io/example/composite-sdk:latest"
```

Only `spec.injector.image` exists — everything else is up for design.

### Key files

| File | Purpose |
|------|---------|
| `apis/v2alpha1/instrumentation_types.go` | CRD Go types — **edit this to add fields** |
| `apis/v2alpha1/groupversion_info.go` | API group/version registration |
| `config/crd/bases/instrumentation.opentelemetry.io_instrumentations.yaml` | Generated CRD YAML (don't edit directly) |
| `main.go` | Scheme registration |

### After changing CRD types

```bash
make generate   # regenerates zz_generated.deepcopy.go
make manifests  # regenerates CRD YAML
```

Use [kubebuilder markers](https://book.kubebuilder.io/reference/markers) for validation on new fields.

## TODO

- [ ] Design full CRD spec (Jack)
- [ ] Injector init container integration
- [ ] Controller/reconciler
- [ ] Webhook/validation
- [ ] Status subresource
