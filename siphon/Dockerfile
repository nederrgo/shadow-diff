FROM golang:1.25 AS builder
ARG TARGETOS
ARG TARGETARCH

RUN apt-get update && apt-get install -y --no-install-recommends gcc libc6-dev libpcap-dev && rm -rf /var/lib/apt/lists/*

WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download
COPY . .

RUN CGO_ENABLED=1 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -tags pcap,kernel_bpf -o /siphon-agent ./cmd/agent

# Privileged DaemonSet needs root/CAP_NET_RAW for AF_PACKET capture.
# CGO-linked binary requires glibc (not Alpine musl).
FROM debian:bookworm-slim
WORKDIR /
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates libpcap0.8 && rm -rf /var/lib/apt/lists/*
COPY --from=builder /siphon-agent .
EXPOSE 8080
ENTRYPOINT ["/siphon-agent"]
