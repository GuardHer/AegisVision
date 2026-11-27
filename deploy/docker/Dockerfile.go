# Multi-stage pure Go Dockerfile (CGO_ENABLED=0) for all 5 platform Go services.
# Usage: docker build -f deploy/docker/Dockerfile.go --build-arg SERVICE=aegis-api -t aegisvision/aegis-api:latest .

FROM golang:1.24-alpine AS builder

ARG SERVICE=aegis-api
ARG GOPROXY=https://goproxy.cn,direct
ENV GOPROXY=${GOPROXY}
ENV CGO_ENABLED=0

WORKDIR /src/backend
COPY backend/go.mod backend/go.sum ./
RUN go mod download

COPY backend/ ./
RUN go build -trimpath -ldflags="-s -w" -o /out/service ./cmd/${SERVICE}

FROM alpine:3.21 AS runtime

ARG INSTALL_FFMPEG=false
RUN apk add --no-cache ca-certificates tzdata && \
    if [ "${INSTALL_FFMPEG}" = "true" ]; then apk add --no-cache ffmpeg; fi && \
    adduser -D -u 10001 aegis

WORKDIR /app
COPY --from=builder /out/service /app/service
USER aegis

ENTRYPOINT ["/app/service"]
