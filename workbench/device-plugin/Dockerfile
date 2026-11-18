# Kaniko clones the repo into a scratch context and runs this Dockerfile with
# the app directory as build context. Static build for distroless.
FROM mirror.gcr.io/library/golang:1.27-alpine@sha256:eb61c3873c97416fff0116553fff65d67a3549f72e11d9758849a689cdd8b523 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/device-plugin .

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
# config.yaml is read from the working directory; the chart mounts it at /app/config.yaml
WORKDIR /app
COPY --from=build /out/device-plugin /usr/local/bin/device-plugin
ENTRYPOINT ["/usr/local/bin/device-plugin"]
