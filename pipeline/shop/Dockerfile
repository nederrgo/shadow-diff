# Build context must be pipeline/ (parent of shop/) so replace ../pkg/* resolves.
#   make docker-build  →  docker build -f Dockerfile -t … ..
FROM golang:1.26-alpine AS builder
WORKDIR /src
COPY pkg/beruclient /src/pkg/beruclient
COPY pkg/s3utils /src/pkg/s3utils
COPY pkg/trace /src/pkg/trace
COPY shop/go.mod shop/go.sum /src/shop/
WORKDIR /src/shop
RUN go mod download
COPY shop/ /src/shop/
RUN CGO_ENABLED=0 go build -o /shop ./cmd/shop

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /shop /shop
ENTRYPOINT ["/shop"]
