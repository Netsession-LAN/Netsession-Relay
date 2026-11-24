# Netsession Relay image: one static binary, two services (engine, web).
# Result has no shell (distroless static). Build: docker build --build-arg VERSION=... --build-arg COMMIT=... .

# Base images are pinned by digest so a build is reproducible; the tag stays for readability. Dependabot (docker
# ecosystem) proposes new digests. Look one up by hand: docker buildx imagetools inspect golang:1.26
FROM golang:1.26@sha256:4165ee6bf3b7811fd7b40373290405c83d0167dcb87ee5f29c427cdcef45ff8f AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG COMMIT=unknown
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
      -o /out/nsrelay ./cmd/nsrelay \
 && mkdir -p /out/dirs/var/lib/nsrelay /out/dirs/var/lib/nsrelay-web /out/dirs/run/nsrelay /out/dirs/backups

FROM gcr.io/distroless/static-debian12:latest@sha256:d75cdd72874d4790092fcb1b058493ecf6bb5bf2b2b897045b00ff01d91843f2
COPY --from=build /out/nsrelay /usr/local/bin/nsrelay
# engine state (root), web data + backups (uid 10001); named volumes inherit this ownership
COPY --from=build /out/dirs/var/lib/nsrelay /var/lib/nsrelay
COPY --from=build --chown=10001:10001 /out/dirs/var/lib/nsrelay-web /var/lib/nsrelay-web
COPY --from=build --chown=0:10001 /out/dirs/run/nsrelay /run/nsrelay
COPY --from=build --chown=10001:10001 /out/dirs/backups /backups
# Unprivileged by default (web part, admin commands, the demo). Only the engine needs root for its sockets and
# interfaces; compose.yaml starts it with user "0:0" and a short list of capabilities.
USER 10001:10001
ENTRYPOINT ["/usr/local/bin/nsrelay"]
CMD ["version"]
