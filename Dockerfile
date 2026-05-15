# Build stage
FROM golang:1.26-alpine AS builder
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
ENV LLM_GATEWAY_DB_PATH=/data/state.db
