# Multi-stage: Go binary + Pixie CLI. Nonroot, no kubectl.
FROM golang:1.26 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download
COPY . .

RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -o /pixie-gate ./cmd/pixie-gate

FROM debian:bookworm-slim
ARG TARGETARCH
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl \
	&& rm -rf /var/lib/apt/lists/* \
	&& arch="${TARGETARCH:-amd64}" \
	&& case "$arch" in amd64) px_arch=amd64 ;; arm64) px_arch=arm64 ;; *) px_arch=amd64 ;; esac \
	&& curl -fsSL "https://storage.googleapis.com/pixie-dev-public/cli/latest/cli_linux_${px_arch}" -o /usr/local/bin/px \
	&& chmod +x /usr/local/bin/px \
	&& mkdir -p /home/nonroot /tmp/pixie-gate /etc/pixie-gate \
	&& chown -R 65532:65532 /home/nonroot /tmp/pixie-gate

WORKDIR /
COPY --from=builder /pixie-gate /pixie-gate

# Nonroot UID matches Kubernetes runAsUser 65532
USER 65532:65532
ENV HOME=/home/nonroot
ENV PIXIE_GATE_STATE_DIR=/tmp/pixie-gate
ENV PIXIE_PXL_TEMPLATE_DIR=/etc/pixie-gate
EXPOSE 8081
ENTRYPOINT ["/pixie-gate"]
