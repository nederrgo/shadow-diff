# Build context must be pipeline/ so replace ../pkg/beruclient resolves.
#   make docker-build  →  docker build -f Dockerfile -t … ..
FROM golang:1.26 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /src
COPY pkg/beruclient /src/pkg/beruclient
COPY shadow-soldier/go.mod shadow-soldier/go.sum /src/shadow-soldier/
WORKDIR /src/shadow-soldier
RUN go mod download
COPY shadow-soldier/ /src/shadow-soldier/

RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o /shadow-soldier ./cmd/shadow-soldier

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /shadow-soldier .
USER 65532:65532
ENTRYPOINT ["/shadow-soldier"]
