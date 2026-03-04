FROM python:3.14.3-trixie@sha256:d4be1c97d3cf8ca128c41cf90d2b10c2830117984dd5314c9e846e28a81533ce AS build-python-glibc
WORKDIR /operator-build
COPY requirements.txt .
COPY build_dep_tree.sh .
COPY deps_analyser.py .
ENV PIP_DISABLE_PIP_VERSION_CHECK=1
RUN pip install --root-user-action ignore --target . -r requirements.txt
RUN ./build_dep_tree.sh
RUN python ./deps_analyser.py
RUN rm -rf build_dep_tree.sh deps_analyser.py dependency-tree.txt .dep_tree_env
COPY sitecustomize.py .

FROM python:3.14.3-alpine3.23@sha256:faee120f7885a06fcc9677922331391fa690d911c020abb9e8025ff3d908e510 AS build-python-musl
WORKDIR /operator-build
COPY requirements.txt .
COPY build_dep_tree.sh .
COPY deps_analyser.py .
ENV PIP_DISABLE_PIP_VERSION_CHECK=1
RUN pip install --root-user-action ignore --target . -r requirements.txt
RUN ./build_dep_tree.sh
RUN python ./deps_analyser.py
RUN rm -rf build_dep_tree.sh deps_analyser.py dependency-tree.txt .dep_tree_env
COPY sitecustomize.py .

FROM alpine:3.23.3@sha256:25109184c71bdad752c8312a8623239686a9a2071e8825f20acb8f2198c3f659

# After cp -r /autoinstrumentation/. /otel, the injector finds the Python SDK at:
#   /otel/python/glibc  (glibc systems)
#   /otel/python/musl   (musl/Alpine systems)
#   (via otelinject.conf default or PYTHON_AUTO_INSTRUMENTATION_AGENT_PATH_PREFIX)
RUN mkdir -p /autoinstrumentation/python

COPY --from=build-python-glibc /operator-build/ /autoinstrumentation/python/glibc
COPY --from=build-python-musl  /operator-build/ /autoinstrumentation/python/musl

CMD ["true"]
