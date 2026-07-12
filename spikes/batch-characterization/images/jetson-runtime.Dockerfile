FROM nvcr.io/nvidia/l4t-tensorrt:r8.5.2-runtime

# TODO(internal): Publish a JetPack/L4T-specific audited runtime image and bind
# it to DeviceClass/runtime compatibility instead of importing a host wheel.
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y \
      python3 python3-pip libgomp1 && rm -rf /var/lib/apt/lists/*
COPY --from=ort-wheel onnxruntime_gpu-1.16.0-cp38-cp38-linux_aarch64.whl /tmp/onnxruntime_gpu-1.16.0-cp38-cp38-linux_aarch64.whl
RUN python3 -m pip install --no-cache-dir numpy==1.24.4 \
      protobuf==4.25.4 flatbuffers coloredlogs sympy packaging \
      /tmp/onnxruntime_gpu-1.16.0-cp38-cp38-linux_aarch64.whl \
 && rm /tmp/onnxruntime_gpu-1.16.0-cp38-cp38-linux_aarch64.whl
COPY spikes/batch-characterization/jetson_runner.py /app/runner.py
ENTRYPOINT ["python3", "/app/runner.py"]
