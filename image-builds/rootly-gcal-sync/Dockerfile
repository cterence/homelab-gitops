FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/rootly-gcal-sync .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/rootly-gcal-sync /rootly-gcal-sync
ENTRYPOINT ["/rootly-gcal-sync"]
