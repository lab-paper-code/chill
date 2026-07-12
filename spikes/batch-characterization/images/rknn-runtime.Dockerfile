FROM python:3.10-slim
# TODO(internal): Replace the generic privileged Python image with an audited
# RK3588 runtime image whose librknnrt/driver compatibility is declared.

RUN pip install --no-cache-dir numpy==2.2.6 rknn-toolkit-lite2==2.3.2

WORKDIR /app
COPY spikes/batch-characterization/rknn_runner.py /app/runner.py

ENTRYPOINT ["python3", "/app/runner.py"]
