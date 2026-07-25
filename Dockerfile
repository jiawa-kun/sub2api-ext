# syntax=docker/dockerfile:1
FROM golang:1.22-bookworm AS builder
WORKDIR /src
ENV CGO_ENABLED=0 \
    GOOS=linux \
    GOARCH=amd64 \
    GOPROXY=https://proxy.golang.org,direct
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -ldflags="-s -w" -o /out/sub2api-ext ./cmd/server

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata \
  && adduser -D -H -u 65532 nonroot \
  && mkdir -p /data \
  && chown -R nonroot:nonroot /data
WORKDIR /app
COPY --from=builder /out/sub2api-ext /app/sub2api-ext
COPY configs/config.example.yaml /app/configs/config.yaml
RUN chmod +x /app/sub2api-ext
ENV CONFIG_PATH=/app/configs/config.yaml \
    TZ=Asia/Shanghai
EXPOSE 8090
USER nonroot
ENTRYPOINT ["/app/sub2api-ext"]
