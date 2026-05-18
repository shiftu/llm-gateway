# Build stage
FROM golang:1.26-alpine AS builder
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /bin/llm-gateway ./cmd/llm-gateway

# Final stage — minimal scratch image
FROM scratch
COPY --from=builder /bin/llm-gateway /llm-gateway
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
EXPOSE 7421
ENTRYPOINT ["/llm-gateway"]
CMD ["start"]
VOLUME ["/data"]
# XDG_CONFIG_HOME=/data makes defaultConfigDir() return /data/llm-gateway,
# so state.db and encryption.key land under the persistent volume.
ENV XDG_CONFIG_HOME=/data
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
  CMD ["/llm-gateway", "health"]
