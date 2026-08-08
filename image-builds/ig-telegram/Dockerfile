# Build stage: compile the Go binary with CGO disabled.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/ig-telegram .

# Runtime stage: Alpine with Chromium for go-rod browser automation.
FROM alpine:3.21
RUN apk add --no-cache chromium nss freetype harfbuzz ca-certificates ttf-freefont
COPY --from=build /out/ig-telegram /usr/local/bin/ig-telegram
ENTRYPOINT ["/usr/local/bin/ig-telegram"]
