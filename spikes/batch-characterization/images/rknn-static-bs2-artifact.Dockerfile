FROM busybox:1.36
# TODO(internal): Publish static variants as distinct ModelArtifacts with an
# explicit supported-batch capability and immutable digest.

COPY mobilenet-v2-100-rk3588-static-bs2.rknn /artifact/model.rknn
