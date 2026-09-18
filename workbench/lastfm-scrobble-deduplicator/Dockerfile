# Build stage: Kaniko clones the repo into a scratch context and runs this
# Dockerfile, so the source tree is available. We compile the Go binary with
# CGO disabled for a static distroless-friendly binary.
FROM golang:1.27-alpine@sha256:e9bbdf282b51ac8b34c46e5f31d2d56e7bad60366c35f08d2f295b921b13388b AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/lastfm-scrobble-deduplicator .

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
WORKDIR /app
COPY --from=build /out/lastfm-scrobble-deduplicator /usr/local/bin/lastfm-scrobble-deduplicator
ENTRYPOINT ["/usr/local/bin/lastfm-scrobble-deduplicator"]
