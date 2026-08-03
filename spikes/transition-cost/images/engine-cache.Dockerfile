FROM busybox:1.36

# Device-specific TensorRT cache bytes are supplied only as an external build
# context. They are retained in the registry, not committed to the repository.
COPY engine-cache/ /engine-cache/
