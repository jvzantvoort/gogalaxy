# Release image, built by GoReleaser (dockers_v2) from the prebuilt binaries:
# the build context holds <os>/<arch>/gogalaxy for every target platform.
# For a local image use "make docker".
FROM alpine:3.22

ARG TARGETPLATFORM

RUN addgroup -S -g 1000 gogalaxy \
 && adduser -S -D -H -u 1000 -G gogalaxy gogalaxy \
 && mkdir -p /data /etc/gogalaxy \
 && chown gogalaxy:gogalaxy /data

COPY $TARGETPLATFORM/gogalaxy /usr/bin/gogalaxy
COPY gogalaxy.example.yaml /etc/gogalaxy/gogalaxy.example.yaml

ENV GOGALAXY_STORAGE_PATH=/data \
    GOGALAXY_SERVER_LISTEN=:8000

USER gogalaxy
VOLUME ["/data"]
EXPOSE 8000

ENTRYPOINT ["/usr/bin/gogalaxy"]
CMD ["serve"]
