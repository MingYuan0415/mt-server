FROM golang:1.26.8-alpine AS build

ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=$GOPROXY

WORKDIR /src
COPY go.* ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal

ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/mt-server ./cmd/mt-server
RUN mkdir -p /out/state \
    && touch /out/state/.volume-initialized \
    && chown -R 65532:65532 /out/state

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/mt-server /mt-server
COPY --from=build --chown=65532:65532 /out/state /var/lib/mt-server

USER 65532:65532
ENV MT_STATE_DIR=/var/lib/mt-server
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD ["/mt-server", "healthcheck"]
ENTRYPOINT ["/mt-server"]
