FROM golang:1.25 AS builder
WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /http-mongo-worker .

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /http-mongo-worker /http-mongo-worker
USER 65532:65532
ENTRYPOINT ["/http-mongo-worker"]
