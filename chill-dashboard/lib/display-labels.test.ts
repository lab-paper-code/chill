import assert from "node:assert/strict"
import test from "node:test"
import { architectureLabel, deviceClassLabel, executionPathLabel, powerModeLabel } from "./display-labels.ts"

test("device classes read in one case", () => {
  assert.equal(deviceClassLabel("jetson-agx-orin-64g"), "Jetson AGX Orin 64GB")
  assert.equal(deviceClassLabel("jetson-orin-nano-8g"), "Jetson Orin Nano 8GB")
  assert.equal(deviceClassLabel("jetson-nano-4g"), "Jetson Nano 4GB")
  assert.equal(deviceClassLabel("edge-server-rtx-2080-ti-64g"), "RTX 2080 Ti 64GB")
  assert.equal(deviceClassLabel("orangepi-5-plus-16g"), "Orange Pi 5 Plus 16GB")
})

test("power mode fixed is translated, device mode names stay", () => {
  assert.equal(powerModeLabel("fixed"), "고정")
  assert.equal(powerModeLabel("MAXN"), "MAXN")
  assert.equal(architectureLabel("amd64"), "x86 64비트")
  assert.equal(architectureLabel("riscv64"), "RISCV64")
})

test("execution paths: spec first, then the name, unknown backends keep the name", () => {
  const model = { spec: {
    artifacts: [{ name: "onnx-arm64", format: "onnx", source: { image: "i", architecture: "arm64", files: [] } }, { name: "onnx-int8-amd64", format: "onnx", source: { image: "i", architecture: "amd64", files: [] } }],
    executionPaths: [{ name: "fast", artifact: "onnx-arm64", runtime: { family: "firstfloor", backend: "ort-cuda" } }, { name: "slow", artifact: "onnx-int8-amd64", runtime: { family: "firstfloor", backend: "ort-cpu" } }],
  } }
  assert.equal(executionPathLabel("fast", model), "CUDA ARM 64비트")
  assert.equal(executionPathLabel("slow", model), "CPU 정수 8비트 x86 64비트")
  assert.equal(executionPathLabel("firstfloor-ort-cuda-arm64"), "CUDA ARM 64비트")
  assert.equal(executionPathLabel("firstfloor-ort-cpu-int8-amd64", model), "CPU 정수 8비트 x86 64비트")
  assert.equal(executionPathLabel("custom-path"), "custom-path")
})
