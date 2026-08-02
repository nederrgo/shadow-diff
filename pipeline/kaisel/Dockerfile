# Build context is the repo root (docker build -f pipeline/kaisel/Dockerfile ../..),
# so replace directives for monarch and sample resolve under /workspace.
FROM golang:1.26 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace

# Shared modules monarch replace-points at (must exist before go mod download).
COPY pipeline/pkg/s3utils/ ./pkg/s3utils/
COPY pipeline/pkg/sample/ ./pkg/sample/
COPY pipeline/pkg/monarchpb/ ./pkg/monarchpb/
COPY pipeline/pkg/shadowspec/ ./pkg/shadowspec/

# Download monarch deps first (layer-cache friendly: rarely changes)
COPY pipeline/monarch/go.mod pipeline/monarch/go.sum ./monarch/
RUN cd monarch && go mod download

# Download kaisel deps (monarch + sample + s3utils present for replace paths)
COPY pipeline/kaisel/go.mod pipeline/kaisel/go.sum ./kaisel/
RUN cd kaisel && go mod download

# Copy full source for both modules
COPY pipeline/monarch/ ./monarch/
COPY pipeline/kaisel/ ./kaisel/

RUN cd kaisel && \
    CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -o /kaisel ./cmd/kaisel

# kaisel runs as root (uid 0): required for CAP_BPF / CAP_NET_RAW on kernels < 5.11.
# The DaemonSet security context drops all capabilities except BPF, NET_RAW, PERFMON.
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y iproute2 && rm -rf /var/lib/apt/lists/*
WORKDIR /
COPY --from=builder /kaisel /kaisel
ENTRYPOINT ["/kaisel"]
