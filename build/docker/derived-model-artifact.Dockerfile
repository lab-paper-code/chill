# syntax=docker/dockerfile:1

# BASE_IMAGE must be an exact runnable image digest that supplies the shell and
# copy tools required by the artifact init containers. Dataset carriers use a
# multi-arch image index digest and build for linux/amd64,linux/arm64.
ARG BASE_IMAGE
FROM ${BASE_IMAGE}

# The build context's artifact/ directory becomes /artifact/: model files,
# preprocessor directories, or dataset directories.
COPY artifact/ /artifact/
