# Injection Flow

The instrumentation system spans **two repos** that connect at runtime via environment variables.

## Repos and branches

| Repo | Branch | Role |
|------|--------|------|
| [`grafana/opentelemetry-operator`](https://github.com/grafana/opentelemetry-operator) | `hackathon-16-composite-sdk-injection` | Kubernetes operator — mutates pod specs at admission time |
| [`grafana/opentelemetry-injector`](https://github.com/grafana/opentelemetry-injector) | `hackathon-16-mode-support` | LD_PRELOAD injector binary — runs inside the container before `main()` |

## The injection flow

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

## Key environment variables (operator → injector)

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

## Building the injector from source

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

## Testing mode conflict detection end-to-end

Current e2e tests (`tests/e2e-instrumentation/injector-mode/`) verify **pod mutation** — the operator sets the right env vars and volumes. They use `busybox:latest` as the injector image since no actual injection happens.

To test that `install` vs `install_unless_conflict` **actually behave differently at runtime**, you need:

1. **Injector built from source** (with mode support from `hackathon-16-mode-support`)
2. **A Java app with a pre-existing `-javaagent`** (e.g. Prometheus JMX exporter) — this is the "conflict"
3. **Two test cases:**
   - `mode: install_unless_conflict` + foreign `-javaagent` → injector detects conflict, backs off, `JAVA_TOOL_OPTIONS` keeps only the foreign agent
   - `mode: install` + foreign `-javaagent` → injector forces injection, `JAVA_TOOL_OPTIONS` has both agents

This requires the full build + kind cluster flow described in `local-testing-v2alpha1.md`.
