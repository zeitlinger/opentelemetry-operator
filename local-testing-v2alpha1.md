# Image Volumes — Local Testing (v2alpha1)

This documents local testing of image volume injection for the **v2alpha1 composite SDK injector** API
(`internal/injector/`).

The v2alpha1 injector requires:

- **Kubernetes 1.31+** — when the `ImageVolume` feature gate was introduced (alpha)
- **containerd 2.1+** — required by the kubelet to actually mount image volumes
- **`ImageVolume` feature gate** — must be enabled on the API server and kubelet

> **Note for kind:** `kindest/node:v1.31.x` ships with containerd 1.7.x which predates image volume
> support. Use `kindest/node:v1.32.x` or later, which bundles containerd 2.1.

## Create the kind cluster

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
  - |
    kind: KubeletConfiguration
    featureGates:
      ImageVolume: true
EOF
```

## Install cert-manager

```bash
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.19.4/cert-manager.yaml
kubectl wait --for=condition=Available deployments/cert-manager -n cert-manager --timeout=120s
```

## Build and deploy the operator

```bash
IMG=opentelemetry-operator:dev make container
kind load docker-image opentelemetry-operator:dev --name otel-operator-dev
IMG=opentelemetry-operator:dev make deploy
kubectl rollout status deployment/opentelemetry-operator-controller-manager \
  -n opentelemetry-operator-system --timeout=120s
```

## Build and load the injector images

The v2alpha1 injector uses separate images for the injector binary and each language agent:

```bash
docker build --load -t injector:dev          images/injector
docker build --load -t injector-dotnet:dev   images/injector-dotnet
docker build --load -t injector-java:dev     images/injector-java
docker build --load -t injector-nodejs:dev   images/injector-nodejs
docker build --load -t injector-python:dev   images/injector-python

for img in injector injector-dotnet injector-java injector-nodejs injector-python; do
  kind load docker-image ${img}:dev --name otel-operator-dev
done
```

This builds and loads:
- `injector:dev` — the injector binary (`libotelinject.so` + `otelinject.conf`), mounted at `/otel`
- `injector-dotnet:dev` — .NET agent, mounted at `/otel-dotnet`
- `injector-java:dev` — Java agent (`javaagent.jar`), mounted at `/otel-java`
- `injector-nodejs:dev` — Node.js agent (`register.js`), mounted at `/otel-nodejs`
- `injector-python:dev` — Python agent, mounted at `/otel-python`

## Deploy the OpenTelemetry Collector

Deploy a collector that receives OTLP over HTTP and logs telemetry to stdout:

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

## Create the Instrumentation CR

The v2alpha1 `Instrumentation` CR uses rules-based selection instead of per-pod annotations.
Use the local image tags built in the previous step:

```bash
kubectl apply -f - <<EOF
apiVersion: instrumentation.opentelemetry.io/v2alpha1
kind: Instrumentation
metadata:
  name: test-instrumentation
spec:
  injector: injector:dev
  dotnet: injector-dotnet:dev
  java: injector-java:dev
  nodejs: injector-nodejs:dev
  python: injector-python:dev
  rules:
    - name: catch-all
      config:
        env:
          - name: OTEL_EXPORTER_OTLP_ENDPOINT
            value: http://otelcol-collector:4318
EOF
```

## Test Python

Create application pod:

```bash
kubectl apply -f - <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: test-python-app
spec:
  containers:
  - name: app
    image: python:3.11-slim
    command: ["sleep", "infinity"]
EOF
kubectl wait --for=condition=Ready pod/test-python-app --timeout=120s
```

Verify the image volumes were injected:

```bash
kubectl get pod test-python-app -o json | python3 -c "
import json, sys
pod = json.load(sys.stdin)
print('=== Volumes ===')
for v in pod['spec']['volumes']:
    print(' ', v['name'], '->', list(v.keys()))
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
  otel-injector -> ['image', 'name']
  otel-injector-java -> ['image', 'name']
  otel-injector-nodejs -> ['image', 'name']
  otel-injector-python -> ['image', 'name']
  otel-injector-dotnet -> ['image', 'name']

=== Volume mounts ===
  /var/run/secrets/kubernetes.io/serviceaccount <- kube-api-access-xxxxx
  /otel <- otel-injector
  /otel-java <- otel-injector-java
  /otel-nodejs <- otel-injector-nodejs
  /otel-python <- otel-injector-python
  /otel-dotnet <- otel-injector-dotnet
```

Verify `LD_PRELOAD` and the Python agent path env var:

```bash
kubectl exec test-python-app -- env | grep -E "LD_PRELOAD|PYTHON_AUTO"
```

Expected:
```
LD_PRELOAD=/otel/autoinstrumentation/libotelinject.so
PYTHON_AUTO_INSTRUMENTATION_AGENT_PATH_PREFIX=/otel-python/autoinstrumentation/python
```

Send a trace and verify it appears in the collector logs:

```bash
# Watch collector in one terminal
kubectl logs deployment/otelcol-collector -f

# Trigger HTTP activity in another terminal
kubectl exec test-python-app -- python3 -c "
import urllib.request
urllib.request.urlopen('http://example.com')
print('done')
"
```

You should see a trace with spans for the outgoing HTTP request appear in the collector logs.

## Cleanup

```bash
kind delete cluster --name otel-operator-dev
```
