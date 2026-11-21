FROM golang:1.26.11-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/ ./cmd/luxmed-watcher ./cmd/luxmed-mcp

FROM alpine:3.21
RUN adduser -D -h /app appuser && mkdir -p /app/data && chown -R appuser:appuser /app
WORKDIR /app
COPY --from=build /out/ /usr/local/bin/
USER appuser
VOLUME ["/app/data"]
EXPOSE 8080
ENTRYPOINT ["luxmed-watcher"]
