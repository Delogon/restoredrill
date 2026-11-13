# Build restoredrill from source. Used by the demo compose and local builds;
# releases use Dockerfile.goreleaser with the GoReleaser-built binary.
#
#   docker build -t ghcr.io/restoredrill/restoredrill:latest .
#
# The runtime image bundles restic and Borg, so drills (and the demo seed)
# work without installing anything else. The binary path matches the probe
# container expectation (internal/sandbox: /usr/local/bin/restoredrill).

FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=0.1.0-dev
ARG COMMIT=none
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
    -o /out/restoredrill ./cmd/restoredrill

FROM alpine:3.22
# restic >= 0.17 (JSON restore output) and Borg 1.2/1.4 are required at run
# time; tzdata serves scheduler timezones, openssh-client restic's sftp backend.
RUN apk add --no-cache restic borgbackup ca-certificates tzdata openssh-client \
    && restic version && borg --version
COPY --from=build /out/restoredrill /usr/local/bin/restoredrill
ENTRYPOINT ["/usr/local/bin/restoredrill"]
CMD ["serve"]