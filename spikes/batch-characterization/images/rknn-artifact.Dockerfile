FROM busybox:1.36
# TODO(internal): Replace hand-built artifact carrier images with immutable
# ModelArtifact storage references and verified fetch/init behavior.

COPY models/rknn/mobilenet-v2-100-rk3588-bs1.rknn /artifact/bs1.rknn
COPY models/rknn/mobilenet-v2-100-rk3588-bs4.rknn /artifact/bs4.rknn
COPY models/rknn/mobilenet-v2-100-rk3588-bs16.rknn /artifact/bs16.rknn
