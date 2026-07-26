# Build context is the repo root (docker build -f pipeline/kaisel/Dockerfile ../..),
# so the replace directive replace github.com/shadow-diff/monarch => ../monarch
# resolves correctly inside the container at /workspace/kaisel/../monarch.
FROM golang:1.26 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace

# Download monarch deps first (layer-cache friendly: rarely changes)
COPY pipeline/monarch/go.mod pipeline/monarch/go.sum ./monarch/
RUN cd monarch && go mod download

# Download kaisel deps (monarch source not needed yet, only go.mod/go.sum)
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
