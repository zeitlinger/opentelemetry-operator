ARG JAVA_SDK_VERSION=v2.23.0

FROM alpine:3.23.3@sha256:25109184c71bdad752c8312a8623239686a9a2071e8825f20acb8f2198c3f659 AS download-java-sdk
ARG JAVA_SDK_VERSION

RUN apk add --no-cache curl

RUN curl -sSfL -o /opentelemetry-javaagent.jar \
    "https://github.com/open-telemetry/opentelemetry-java-instrumentation/releases/download/${JAVA_SDK_VERSION}/opentelemetry-javaagent.jar"

FROM alpine:3.23.3@sha256:25109184c71bdad752c8312a8623239686a9a2071e8825f20acb8f2198c3f659

# After cp -r /autoinstrumentation/. /otel, the injector finds the agent at /otel/javaagent.jar
# Matches the existing autoinstrumentation/java image convention (/javaagent.jar).
# Operator sets: JVM_AUTO_INSTRUMENTATION_AGENT_PATH=/otel/javaagent.jar
RUN mkdir -p /autoinstrumentation

COPY --from=download-java-sdk /opentelemetry-javaagent.jar /autoinstrumentation/javaagent.jar

CMD ["true"]
