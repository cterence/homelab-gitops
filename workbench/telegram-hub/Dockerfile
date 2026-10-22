# Build stage: Kaniko clones the repo into a scratch context and runs this
# Dockerfile, so the source tree is available. We compile the Go binary with
# CGO disabled for a static binary.
FROM mirror.gcr.io/library/golang:1.27-alpine@sha256:d530cf60723abf1ffae0cd16bb546b379ec0550d2f3815e5b16ed69dbc00fba4 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/telegram-hub .

# The runtime needs ffmpeg to remux generated audio, so the final image is
# alpine with the ffmpeg package instead of distroless.
FROM mirror.gcr.io/library/alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6
RUN apk add --no-cache ffmpeg
# The generated/ cache directory is a PVC mounted at /app/generated.
WORKDIR /app
COPY --from=build /out/telegram-hub /usr/local/bin/telegram-hub
ENTRYPOINT ["/usr/local/bin/telegram-hub"]

LABEL org.opencontainers.container.app="telegram-hub"
