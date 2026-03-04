ARG DOTNET_SDK_VERSION=v1.11.0

FROM alpine:3.23.3@sha256:25109184c71bdad752c8312a8623239686a9a2071e8825f20acb8f2198c3f659 AS download-dotnet-sdk
ARG DOTNET_SDK_VERSION
ARG TARGETARCH

RUN apk add --no-cache curl unzip

RUN case ${TARGETARCH} in \
    amd64) export ARCH="x64" ;; \
    arm64) export ARCH="arm64" ;; \
    *)     echo "architecture unsupported: ${TARGETARCH}" && exit 1 ;; \
  esac && \
  curl -sSfL -o dotnet-instrumentation.zip \
    "https://github.com/open-telemetry/opentelemetry-dotnet-instrumentation/releases/download/${DOTNET_SDK_VERSION}/opentelemetry-dotnet-instrumentation-linux-glibc-${ARCH}.zip" && \
  mkdir -p /glibc && \
  unzip -q dotnet-instrumentation.zip -d /glibc && \
  curl -sSfL -o dotnet-instrumentation-musl.zip \
    "https://github.com/open-telemetry/opentelemetry-dotnet-instrumentation/releases/download/${DOTNET_SDK_VERSION}/opentelemetry-dotnet-instrumentation-linux-musl-${ARCH}.zip" && \
  mkdir -p /musl && \
  unzip -q dotnet-instrumentation-musl.zip -d /musl

FROM alpine:3.23.3@sha256:25109184c71bdad752c8312a8623239686a9a2071e8825f20acb8f2198c3f659

# After cp -r /autoinstrumentation/. /otel, the injector finds the .NET SDK at:
#   /otel/dotnet/glibc  (glibc systems)
#   /otel/dotnet/musl   (musl/Alpine systems)
#   (via otelinject.conf default or DOTNET_AUTO_INSTRUMENTATION_AGENT_PATH_PREFIX)
RUN mkdir -p /autoinstrumentation/dotnet

COPY --from=download-dotnet-sdk /glibc /autoinstrumentation/dotnet/glibc
COPY --from=download-dotnet-sdk /musl  /autoinstrumentation/dotnet/musl

CMD ["true"]
