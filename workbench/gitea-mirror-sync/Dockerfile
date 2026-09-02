# Build stage: Kaniko clones the repo into a scratch context and runs this
# Dockerfile, so the source tree is available. We compile the Go binary with
# CGO disabled for a static distroless-friendly binary.
FROM golang:1.27-alpine@sha256:26402d86be3d72e6a9410afa0108f03529f51f0c1b5eb7f503d0bc44cc7857ac AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/gitea-mirror-sync .

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/gitea-mirror-sync /usr/local/bin/gitea-mirror-sync
ENTRYPOINT ["/usr/local/bin/gitea-mirror-sync"]
