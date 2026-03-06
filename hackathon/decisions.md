# Decisions

## Mar 4 sync

1. **Use existing composite images for hackathon** — env var overrides point the injector to the right SDK paths. Split images deferred to post-hackathon.
2. **Tri-state instrumentation mode** — `config.disabled: bool` → `config.mode: install|skip|install_unless_conflict` with `install_unless_conflict` as default. Also settable as CR-wide default at `spec.defaults.mode`.
3. **Temporary language-specific images** (Jack) — copy existing operator images with file layout adjusted to match injector expectations.
4. **Python image restructuring → injector SIG** (Nikola) — propose env var overrides per glibc flavor and standardize Python layout to match .NET.
5. **Crash-loop auto-recovery** (stretch goal) — operator watches k8s events for restart counts / failure reasons and avoids re-instrumenting failing pods.
6. **Operator internal telemetry** — export instrumentation status metrics via OTel collector, scrapeable by Prometheus for a community dashboard.

## Future work (post-hackathon)

- [ ] **Node.js conflict detection** (Nikola) — upstream equivalent of Python's `sitecustomize` safety checks for Node.js
- [ ] **Python image restructuring** (Nikola → injector SIG) — standardize Python image to match .NET layout (both glibc+musl in one dir); propose env var overrides per glibc flavor to injector SIG
- [ ] **Split language images** — decompose composite image into individual shareable images (deferred from hackathon, depends on injector SIG alignment)
