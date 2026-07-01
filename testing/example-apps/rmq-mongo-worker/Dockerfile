FROM golang:1.25 AS builder
WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /rmq-mongo-worker .

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /rmq-mongo-worker /rmq-mongo-worker
USER 65532:65532
ENTRYPOINT ["/rmq-mongo-worker"]
