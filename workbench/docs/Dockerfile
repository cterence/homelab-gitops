# Build stage: compile Hugo from source with the extended tag (libsass, needed
# by the hextra theme), then render the site. The theme is fetched as a Hugo
# module through the Go module proxy, so nothing is vendored in this repo. The
# Hugo module loader shells out to git to resolve versions.
FROM mirror.gcr.io/library/golang:1.27-alpine@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb AS build
RUN apk add --no-cache build-base libsass-dev git
RUN CGO_ENABLED=1 go install --tags extended github.com/gohugoio/hugo@v0.169.0
WORKDIR /src
COPY . .
RUN hugo --minify

FROM mirror.gcr.io/library/httpd:2.4.65-bookworm@sha256:fbc12199ccad031d8047e9c789d65aceee2d14f99ba90664cd3a3996867a5582
COPY --from=build /src/public /usr/local/apache2/htdocs
EXPOSE 80
