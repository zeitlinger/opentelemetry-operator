# v1alpha1 Comparison

Reviewed all env vars injected by v1alpha1 (`internal/instrumentation/sdk.go`) vs v2alpha1. Status:

## Intentionally different (handled by injector binary at runtime)

- Language-specific vars (JAVA_TOOL_OPTIONS, NODE_OPTIONS, PYTHONPATH, .NET profiling) — injector binary does language detection
- `OTEL_RESOURCE_ATTRIBUTES` — injector binary builds from `OTEL_INJECTOR_*` vars at runtime
- `OTEL_SERVICE_NAME` — injector binary sets from `OTEL_INJECTOR_SERVICE_NAME`

## Intentionally different (user sets via `config.env`)

- `OTEL_EXPORTER_OTLP_ENDPOINT` — no dedicated CRD field, user provides in env
- `OTEL_PROPAGATORS` — user provides in env (v1alpha1 has `Spec.Propagators`)
- `OTEL_TRACES_SAMPLER` / `OTEL_TRACES_SAMPLER_ARG` — user provides in env (v1alpha1 has `Spec.Sampler`)
- TLS certs (`OTEL_EXPORTER_OTLP_CERTIFICATE` etc.) — user provides in env; volume mounts for cert files not yet supported (see [known gaps](schema.md#known-gaps-future-work))

## Not porting (intentional)

- Annotation-based resource attributes (`resource.opentelemetry.io/*`) — v1alpha1 workaround for limited config model. v2alpha1 users set these via `config.env` on rules, which is more explicit and auditable.
