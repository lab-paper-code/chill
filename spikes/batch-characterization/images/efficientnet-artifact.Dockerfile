FROM busybox:1.36

# TODO(internal): Publish through ModelArtifact storage/import with source digest.
COPY models/efficientnet-b4.onnx /artifact/model.onnx
