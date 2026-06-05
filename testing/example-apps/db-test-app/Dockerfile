FROM golang:1.22 AS builder
ARG TARGETOS=linux
ARG TARGETARCH=amd64

WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download
COPY . .

ENV CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH}
RUN go build -trimpath -ldflags="-s -w" -o /db-test-app .

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /db-test-app /db-test-app
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/db-test-app"]
