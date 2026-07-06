FROM golang:1.25-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /shop ./cmd/shop

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /shop /shop
ENTRYPOINT ["/shop"]
