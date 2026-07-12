FROM python:3.11-slim
# TODO(internal): Build from the resolved ModelArtifact runtime contract, pin
# all base/package digests, and publish through the CHILL multi-arch pipeline.

RUN pip install --no-cache-dir numpy==2.2.6 onnxruntime==1.23.2

WORKDIR /app
COPY spikes/batch-characterization/runner.py /app/runner.py

RUN useradd --system --uid 65532 --create-home runner
USER 65532:65532

ENTRYPOINT ["python3", "/app/runner.py"]
