FROM python:3.10-slim
# TODO(internal): This is an artifact-builder image, not a serving runtime.
# Pin the official toolkit wheel and dependencies by digest and retain build provenance.

RUN pip install --no-cache-dir \
      'numpy<=1.26.4' 'protobuf>=4.21.6,<=4.25.4' onnx==1.16.1 \
      psutil ruamel.yaml scipy tqdm fast-histogram \
      opencv-python-headless onnxruntime \
 && pip install --no-cache-dir --index-url https://download.pytorch.org/whl/cpu torch==2.4.0 \
 && pip install --no-cache-dir --no-deps \
      https://raw.githubusercontent.com/airockchip/rknn-toolkit2/master/rknn-toolkit2/packages/x86_64/rknn_toolkit2-2.3.2-cp310-cp310-manylinux_2_17_x86_64.manylinux2014_x86_64.whl

COPY spikes/batch-characterization/export_rknn_dynamic.py /app/export.py
ENTRYPOINT ["python3", "/app/export.py"]
