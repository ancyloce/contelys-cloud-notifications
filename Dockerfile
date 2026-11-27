# contelys/notifications (Notifications ADR 0002): the binary, built in a Go stage on Debian pinned
# by digest, in a final stage FROM scratch with the public root certificates of the stage's
# ca-certificates package, copied unmodified as a separate file with its licence and its package's
# name and version.
FROM golang:1.27.0-trixie@sha256:df98008ecd2b0ecc9f0a94d1b07e3564a9c92b555369b33d9b5f60d0765b2db7 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN GOWORK=off go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOWORK=off go build -trimpath -o /out/notifications ./cmd/notifications
RUN mkdir -p /out/doc && cp /usr/share/doc/ca-certificates/copyright /out/doc/copyright \
    && dpkg-query --showformat='${Package} ${Version}\n' --show ca-certificates > /out/doc/package

FROM scratch
COPY --from=build /out/notifications /notifications
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/doc/ /usr/share/doc/ca-certificates/
USER 65532:65532
ENTRYPOINT ["/notifications"]
CMD ["serve", "-config", "/etc/notifications/config.yaml"]
# The health subcommand asks the instance's own readiness endpoint, since the image has no shell.
HEALTHCHECK --interval=10s --timeout=5s --start-period=30s CMD ["/notifications", "health", "-config", "/etc/notifications/config.yaml"]
