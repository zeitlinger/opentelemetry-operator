ARG INJECTOR_VERSION=v0.1.0

FROM alpine:3.23.3@sha256:25109184c71bdad752c8312a8623239686a9a2071e8825f20acb8f2198c3f659 AS download-injector-binary
ARG TARGETARCH
ARG INJECTOR_VERSION

RUN apk add --no-cache wget

RUN case ${TARGETARCH} in \
    amd64) export INJECTOR_LIB="libotelinject_amd64.so" ;; \
    arm64) export INJECTOR_LIB="libotelinject_arm64.so" ;; \
    *)     echo "architecture unsupported: ${TARGETARCH}" && exit 1 ;; \
  esac && \
  wget -q -O /libotelinject.so \
    "https://github.com/open-telemetry/opentelemetry-injector/releases/download/${INJECTOR_VERSION}/${INJECTOR_LIB}"

FROM alpine:3.23.3@sha256:25109184c71bdad752c8312a8623239686a9a2071e8825f20acb8f2198c3f659

# The operator's init container runs: cp -r /autoinstrumentation/. /otel
# After the copy, the operator sets:
#   LD_PRELOAD=/otel/libotelinject.so
#   OTEL_INJECTOR_CONFIG_FILE=/otel/injector/otelinject.conf
RUN mkdir -p /autoinstrumentation/injector

COPY --from=download-injector-binary /libotelinject.so /autoinstrumentation/libotelinject.so
COPY otelinject.conf /autoinstrumentation/injector/otelinject.conf

CMD ["true"]
