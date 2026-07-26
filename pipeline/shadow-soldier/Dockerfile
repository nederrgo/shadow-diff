FROM golang:1.26 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download
COPY . .

RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o /shadow-soldier ./cmd/shadow-soldier

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /shadow-soldier .
USER 65532:65532
ENTRYPOINT ["/shadow-soldier"]
