# syntax=docker/dockerfile:1

# BASE_IMAGE must be an exact runnable child-image digest that supplies the
# shell and checksum tools required by the Profiler artifact init container.
ARG BASE_IMAGE
FROM ${BASE_IMAGE}

# The build context supplies one exact derived artifact as a file named
# "artifact". ARTIFACT_NAME is the catalogued path basename.
ARG ARTIFACT_NAME
COPY artifact /artifact/${ARTIFACT_NAME}
