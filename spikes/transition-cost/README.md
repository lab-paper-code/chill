# Transition-cost spike

This directory retains research observations for the specification's Layer-0
state-transition quantities. It is outside the online Profiler path: nothing
here defines a production API, controller, `DeviceProfile`, or accepted
transition-cost record.

Read [METHOD.md](METHOD.md) before running another transition. It defines the
minimum frozen protocol, state-pair, environment, energy-component, and
availability boundaries required for an observation to support causal
comparison.

The first observation discovered which parameters must be frozen. It did not
have a pre-run transition intent, so it remains a diagnostic parameter audit,
not a transition-cost sample. Its retained timestamps and power data still
support baseline-sensitivity analysis and selection of the next controlled
toggle.

Observations:

- [Jetson Xavier NX MobileNet to EfficientNet cold transition](observations/jetson-xavier-nx-mobilenet-to-efficientnet-cold.md)

Frozen controlled follow-up:

- [Jetson Xavier NX cold/warm engine-cache paired intent](intents/jetsonx-mobilenet-to-efficientnet-engine-cache-pair-v1.json)
- The post-hoc cold observation is not retro-label eligible. Execute the frozen
  cold variant before the warm variant; only the target engine-cache presence
  may differ between the two rendered target Pods.
