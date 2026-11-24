# Netsession Relay image: one static binary, two services (engine, web).
# Result has no shell (distroless static). Build: docker build --build-arg VERSION=... --build-arg COMMIT=... .

# Base images are pinned by digest so a build is reproducible; the tag stays for readability. Dependabot (docker
# ecosystem) proposes new digests. Look one up by hand: docker buildx imagetools inspect golang:1.26
FROM golang:1.27@sha256:205c7636d74e0ffbe3d007162df7d74f859f5a8c8322dfb9f907977686c126cf AS build
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
