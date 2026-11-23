# Netsession Relay image: one static binary, two services (engine, web).
# Result has no shell (distroless static). Build: docker build --build-arg VERSION=... --build-arg COMMIT=... .

FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG COMMIT=unknown
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
      -o /out/nsrelay ./cmd/nsrelay \
 && mkdir -p /out/dirs/var/lib/nsrelay /out/dirs/var/lib/nsrelay-web /out/dirs/run/nsrelay /out/dirs/backups

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/nsrelay /usr/local/bin/nsrelay
# engine state (root), web data + backups (uid 10001); named volumes inherit this ownership
COPY --from=build /out/dirs/var/lib/nsrelay /var/lib/nsrelay
COPY --from=build --chown=10001:10001 /out/dirs/var/lib/nsrelay-web /var/lib/nsrelay-web
COPY --from=build --chown=0:10001 /out/dirs/run/nsrelay /run/nsrelay
COPY --from=build --chown=10001:10001 /out/dirs/backups /backups
ENTRYPOINT ["/usr/local/bin/nsrelay"]
CMD ["version"]
