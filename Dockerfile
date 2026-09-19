# Build Stage
FROM --platform=$BUILDPLATFORM golang:alpine AS build-env

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=1.1.0

RUN apk add --no-cache git ca-certificates tzdata

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN GIT_COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo "docker") && \
    BUILD_TIME=$(date -u +"%Y-%m-%dT%H:%M:%SZ") && \
    CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} go build -v \
    -ldflags="-s -w -X 'github.com/esrrhs/connperf/version.Version=${VERSION}' -X 'github.com/esrrhs/connperf/version.GitCommit=${GIT_COMMIT}' -X 'github.com/esrrhs/connperf/version.BuildTime=${BUILD_TIME}'" \
    -o connperf .

# Final Stage
FROM alpine:latest

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

COPY --from=build-env /app/connperf /app/connperf

ENTRYPOINT ["/app/connperf"]
CMD ["-h"]
