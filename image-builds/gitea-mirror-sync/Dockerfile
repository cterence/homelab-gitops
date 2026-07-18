# Build stage: Kaniko clones the repo into a scratch context and runs this
# Dockerfile, so the source tree is available. We compile the Go binary with
# CGO disabled for a static distroless-friendly binary.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/gitea-mirror-sync .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/gitea-mirror-sync /usr/local/bin/gitea-mirror-sync
ENTRYPOINT ["/usr/local/bin/gitea-mirror-sync"]
