# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build

ARG TARGETOS
ARG TARGETARCH
ARG VERSION="0.0.0"
ARG BRANCH="edge"
ARG BUILD_DATE="1/1/1970"

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
      -ldflags "-s -w \
        -X 'github.com/Nexxed/Varken/internal/app.Version=${VERSION}' \
        -X 'github.com/Nexxed/Varken/internal/app.Branch=${BRANCH}' \
        -X 'github.com/Nexxed/Varken/internal/app.BuildDate=${BUILD_DATE}'" \
      -o /out/varken ./cmd/varken

FROM alpine:3.24

ARG VERSION="0.0.0"
ARG BRANCH="edge"
ARG BUILD_DATE="1/1/1970"
ARG VCS_REF=""

# Same defaults as the Python image: debug logging on, configuration in /config. The container runs as root,
# as before, so existing root-owned /config volumes keep working.
ENV DEBUG="True" \
    DATA_FOLDER="/config" \
    VERSION=$VERSION \
    BRANCH=$BRANCH \
    BUILD_DATE=$BUILD_DATE

LABEL maintainer="dirtycajunrice,samwiseg0" \
  org.opencontainers.image.created=$BUILD_DATE \
  org.opencontainers.image.url="https://github.com/Nexxed/Varken" \
  org.opencontainers.image.source="https://github.com/Nexxed/Varken" \
  org.opencontainers.image.version=$VERSION \
  org.opencontainers.image.revision=$VCS_REF \
  org.opencontainers.image.vendor="nexxed" \
  org.opencontainers.image.title="varken" \
  org.opencontainers.image.description="Varken (Nexxed fork, adds Cleanuparr) aggregates data from the Plex ecosystem into InfluxDB using Grafana for a frontend" \
  org.opencontainers.image.licenses="MIT"

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app
COPY --from=build /out/varken /app/varken
COPY data /app/data

# varken writes /config/varken.example.ini itself on start, which the old CMD did with cp.
ENTRYPOINT ["/app/varken"]
