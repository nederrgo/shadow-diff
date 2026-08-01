# Build context must be pipeline/ (parent of monarch/) so replace ../pkg/{s3utils,monarchpb} resolve.
#   make docker-build  →  docker build -f Dockerfile -t … ..
FROM golang:1.26 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /src
COPY pkg/s3utils /src/pkg/s3utils
COPY pkg/monarchpb /src/pkg/monarchpb
COPY monarch/go.mod monarch/go.sum /src/monarch/
WORKDIR /src/monarch
RUN go mod download
COPY monarch/ /src/monarch/

# the GOARCH has no default value to allow the binary to be built according to the host where the command
# was called. For example, if we call make docker-build in a local env which has the Apple Silicon M1 SO
# the docker BUILDPLATFORM arg will be linux/arm64 when for Apple x86 it will be linux/amd64. Therefore,
# by leaving it empty we can ensure that the container and binary shipped on it will have the same platform.
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o manager cmd/main.go

# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /src/monarch/manager .
USER 65532:65532

ENTRYPOINT ["/manager"]
