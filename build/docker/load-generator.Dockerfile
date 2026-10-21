# Python 3.12: the live serving certificate lacks the Subject and Authority Key
# Identifiers that the default strict verification of 3.13 and later requires.
FROM python:3.12-slim

COPY load-generator/ /src/load-generator/
RUN pip install --no-cache-dir /src/load-generator && rm -rf /src
USER 65532:65532

ENTRYPOINT ["chill-loadgen"]
