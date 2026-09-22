# Build stage: Kaniko clones the repo into a scratch context and runs this
# Dockerfile, so the source tree is available. We compile the Go binary with
# CGO disabled for a static binary.
FROM golang:1.26-alpine@sha256:8ac98ca534ac3f51e1f420a1dd2c15e74c75cfa0f23f3ad27eb5d7236c349a0c AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/tts9000 .

# The runtime needs ffmpeg to remux generated audio, so the final image is
# alpine with the ffmpeg package instead of distroless.
FROM alpine:3.22@sha256:5291449c3df73caf6ed85e649dec1b9e818b39a5d8c871e97afc13e9cd5e8fa8
RUN apk add --no-cache ffmpeg
# The generated/ cache directory is a PVC mounted at /app/generated.
WORKDIR /app
COPY --from=build /out/tts9000 /usr/local/bin/tts9000
ENTRYPOINT ["/usr/local/bin/tts9000"]

LABEL org.opencontainers.container.app="tts9000"
