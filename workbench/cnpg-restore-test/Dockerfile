FROM mirror.gcr.io/library/golang:1.27-alpine@sha256:f2c872bcee949b7be9c490efd02b48a8a2fbf947cdf533f1c58525ec9d8170fd AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/cnpg-restore-test .

FROM scratch
ENV HOME=/root
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/cnpg-restore-test /usr/local/bin/cnpg-restore-test
ENTRYPOINT ["/usr/local/bin/cnpg-restore-test"]
