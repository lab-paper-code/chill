# Transition-cost spike

This directory retains research observations for the specification's Layer-0
state-transition quantities. It is outside the online Profiler path: nothing
here defines a production API, controller, `DeviceProfile`, or accepted
transition-cost record.

Read [METHOD.md](METHOD.md) before running another transition. It defines the
minimum frozen protocol, state-pair, environment, energy-component, and
availability boundaries required for an observation to support causal
comparison.

Read [NEXT_PROTOCOL.md](NEXT_PROTOCOL.md) before freezing a repeated pair. The
next family first adds a Plan-03-compatible 30-second model-loaded-idle window
and an external request path; v1 `servingDuring=none` repetitions would not be
poolable with that family.

The first observation discovered which parameters must be frozen. It did not
have a pre-run transition intent, so it remains a diagnostic parameter audit,
not a transition-cost sample. Its retained timestamps and power data still
support baseline-sensitivity analysis and selection of the next controlled
toggle.

Observations:

- [Jetson Xavier NX MobileNet to EfficientNet cold transition](observations/jetson-xavier-nx-mobilenet-to-efficientnet-cold.md)
- [Jetson Xavier NX cold/warm engine-cache paired transition](observations/jetson-xavier-nx-engine-cache-pair-v1.md)

Frozen controlled pair:

- [Jetson Xavier NX cold/warm engine-cache paired intent](intents/jetsonx-mobilenet-to-efficientnet-engine-cache-pair-v1.json)
- The post-hoc cold observation was not retro-label eligible, so both sides
  were rerun under the frozen protocol. The retained pair passed its declared
  identity, environment, thermal, cache-state, and total wall-coverage gates.
- The v1 frozen field named `experimentIdle` is retained byte-for-byte, but its
  no-model condition is now reported as `nodeStandby`. v1 did not measure
  Plan 03's state-specific `modelLoadedIdle` baseline.
