FROM golang:1.25 AS builder
ARG TARGETOS
ARG TARGETARCH

RUN apt-get update && apt-get install -y libpcap-dev gcc

WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download
COPY . .

RUN CGO_ENABLED=1 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -o /siphon ./cmd/siphon

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y libpcap0.8 ca-certificates && rm -rf /var/lib/apt/lists/*
WORKDIR /
COPY --from=builder /siphon .
USER 0:0
EXPOSE 8080
ENTRYPOINT ["/siphon"]
