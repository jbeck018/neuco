FROM golang:1.25-alpine AS base
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .

FROM base AS server-build
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /bin/neuco-api ./cmd/server

FROM base AS worker-build
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /bin/neuco-worker ./cmd/worker

FROM alpine:3.19 AS server
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S neuco && adduser -S neuco -G neuco
COPY --from=server-build /bin/neuco-api /bin/neuco-api
COPY --chown=neuco:neuco migrations/ /app/migrations/
USER neuco
EXPOSE 8080
HEALTHCHECK --interval=15s --timeout=3s --start-period=5s --retries=3 \
    CMD ["wget", "--no-verbose", "--tries=1", "--spider", "http://localhost:8080/health"]
ENTRYPOINT ["/bin/neuco-api"]

FROM alpine:3.19 AS worker
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S neuco && adduser -S neuco -G neuco
COPY --from=worker-build /bin/neuco-worker /bin/neuco-worker
COPY --chown=neuco:neuco migrations/ /app/migrations/
USER neuco
ENTRYPOINT ["/bin/neuco-worker"]
