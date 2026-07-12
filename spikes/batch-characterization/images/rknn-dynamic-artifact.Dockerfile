FROM busybox:1.36
# TODO(internal): Publish this output through the ModelArtifact build/import
# pipeline with source/build/capability metadata; do not rely on /tmp context.

COPY mobilenet-v2-100-rk3588-dynamic-b1-16.rknn /artifact/model.rknn
