# build with the vendored deps, then ship the static binary on alpine: small, but keeps a shell
# so you can `docker exec -it zfs-exporter sh` to poke at things. runs as a non-root user.
# make docker passes VERSION/COMMIT from git; a bare `docker build .` reports "dev".
#
# REGISTRY is where the two base images come from: Google's mirror of Docker Hub, which holds
# the same images, unless a build says otherwise. Docker Hub itself (docker.io/library) limits
# how much an address may pull, which a CI runner's shared address runs into; CI builds again
# from it when the mirror gives nothing. Nothing here needs a newer Dockerfile syntax than every
# builder has, so no syntax image is named: that was a third thing to fetch before a build.
ARG GO_VERSION=1.27
ARG ALPINE_VERSION=3.24
ARG REGISTRY=mirror.gcr.io/library

FROM ${REGISTRY}/golang:${GO_VERSION}-alpine AS build
ARG VERSION=dev
ARG COMMIT=unknown
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -mod=vendor \
      -ldflags "-s -w \
        -X github.com/katbyte/go-kt/version.Version=${VERSION} \
        -X github.com/katbyte/go-kt/version.GitCommit=${COMMIT}" \
      -o /zfs-exporter .

FROM ${REGISTRY}/alpine:${ALPINE_VERSION}
RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -H -u 65532 zfs
COPY --from=build /zfs-exporter /usr/local/bin/zfs-exporter
USER zfs
# it runs the machine's own zpool and zpool_influxdb, so they always match the machine's ZFS: a
# deployment mounts the machine's / read-only at /host and gives the container /dev/zfs, which
# ZFS lets anyone read the pools' state through. The programs are run through the machine's own
# loader and libraries, since alpine has none that would do; see docs/container.md. the
# healthcheck assumes the default port.
ENV ZFS_EXPORTER_ROOT=/host
ENV ZFS_EXPORTER_LISTEN=:9087
EXPOSE 9087
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
  CMD wget -q --spider http://127.0.0.1:9087/healthz || exit 1
ENTRYPOINT ["zfs-exporter"]
CMD ["serve"]
