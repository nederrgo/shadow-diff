# Build context is the repo root:
#   docker build -f pipeline/igrises/igris-rabbitmq/Dockerfile <repo-root>
# Layout mirrors the repo so replace ../../pkg/sample resolves.
FROM golang:1.26 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace

COPY pipeline/pkg/sample/ ./pkg/sample/
COPY pipeline/igrises/igris-rabbitmq/go.mod pipeline/igrises/igris-rabbitmq/go.sum ./igrises/igris-rabbitmq/
RUN cd igrises/igris-rabbitmq && go mod download

COPY pipeline/igrises/igris-rabbitmq/ ./igrises/igris-rabbitmq/

RUN cd igrises/igris-rabbitmq && \
    CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -a -o /igris-rabbitmq ./cmd/igris-rabbitmq

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /igris-rabbitmq .
USER 65532:65532
ENTRYPOINT ["/igris-rabbitmq"]
