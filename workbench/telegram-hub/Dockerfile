# Build stage: Kaniko clones the repo into a scratch context and runs this
# Dockerfile, so the source tree is available. We compile the Go binary with
# CGO disabled for a static binary.
FROM mirror.gcr.io/library/golang:1.27-alpine@sha256:b8c9b82affbbb82a020bbcf998dad2ba9516944ac7e883524642a481a9505e9c AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/telegram-hub .

# The runtime needs ffmpeg to remux generated audio, so the final image is
# alpine with the ffmpeg package instead of distroless.
FROM mirror.gcr.io/library/alpine:3.24@sha256:71e9b7bae160ba167fa17414401b94cbcdaf3c32feeab9298bd794a2a0614c35
RUN apk add --no-cache ffmpeg
# The generated/ cache directory is a PVC mounted at /app/generated.
WORKDIR /app
COPY --from=build /out/telegram-hub /usr/local/bin/telegram-hub
ENTRYPOINT ["/usr/local/bin/telegram-hub"]

LABEL org.opencontainers.container.app="telegram-hub"
