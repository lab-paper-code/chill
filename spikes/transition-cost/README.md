# Transition-cost spike

This directory retains research observations for the specification's Layer-0
state-transition quantities. It is outside the online Profiler path: nothing
here defines a production API, controller, `DeviceProfile`, or accepted
transition-cost record.

The first observation asks only whether one real model replacement is
obviously cheap or large enough to justify continued transition-aware work. It
separates Kubernetes handoff, artifact materialization, ONNX Runtime session
creation, first-inference TensorRT engine construction, readiness, power, and
serving-boundary facts. The result is a preliminary digit check, not the E1
campaign required by the specification.

Observations:

- [Jetson Xavier NX MobileNet to EfficientNet cold transition](observations/jetson-xavier-nx-mobilenet-to-efficientnet-cold.md)
