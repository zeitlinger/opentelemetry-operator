FROM node:20@sha256:94a5714e6b243bf37a1767e637996302fadd9443327f5bdbf0179758fe70cacd AS build

WORKDIR /operator-build
COPY package.json .
COPY tsconfig.json .

RUN npm install

FROM alpine:3.23.3@sha256:25109184c71bdad752c8312a8623239686a9a2071e8825f20acb8f2198c3f659

# Matches the existing autoinstrumentation/nodejs image convention:
#   /autoinstrumentation/register.js  (agent entry point)
#   /autoinstrumentation/node_modules/ (all deps)
# After cp -r /autoinstrumentation/. /otel:
#   /otel/register.js
# Operator sets: NODEJS_AUTO_INSTRUMENTATION_AGENT_PATH=/otel/register.js
RUN mkdir -p /autoinstrumentation

COPY --from=build /operator-build/build/workspace /autoinstrumentation

# Lift register.js to the top level (same as autoinstrumentation/nodejs/Dockerfile)
RUN cp -r /autoinstrumentation/node_modules/@opentelemetry/auto-instrumentations-node/build/src/. /autoinstrumentation
RUN ln /autoinstrumentation/register.js /autoinstrumentation/autoinstrumentation.js

RUN chmod -R go+r /autoinstrumentation

CMD ["true"]
