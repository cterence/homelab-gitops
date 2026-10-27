# Build stage: Kaniko clones the repo into a scratch context and runs this
# Dockerfile, so the source tree is available. We compile the Go binary with
# CGO disabled for a static binary.
FROM mirror.gcr.io/library/golang:1.27-alpine@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/telegram-hub .

# The runtime needs ffmpeg to remux generated audio, so the final image is
# alpine with the ffmpeg package instead of distroless.
FROM mirror.gcr.io/library/alpine:3.24@sha256:be2656061b536c1883c4034790e21d0a09c5aaaf51968b89368a3c0371ea2c9a
RUN apk add --no-cache ffmpeg
# The generated/ cache directory is a PVC mounted at /app/generated.
WORKDIR /app
COPY --from=build /out/telegram-hub /usr/local/bin/telegram-hub
ENTRYPOINT ["/usr/local/bin/telegram-hub"]

LABEL org.opencontainers.container.app="telegram-hub"
