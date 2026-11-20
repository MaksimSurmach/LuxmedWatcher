FROM golang:1.26.11-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY internal/miniapp ./internal/miniapp
COPY cmd/miniapp ./cmd/miniapp
RUN CGO_ENABLED=0 go build -trimpath -o /out/miniapp ./cmd/miniapp

FROM node:24-alpine AS assets
WORKDIR /deps
COPY tools/browser-session/package*.json ./
RUN npm ci --omit=dev --ignore-scripts

FROM alpine:3.21
WORKDIR /app
RUN apk add --no-cache ca-certificates
COPY --from=build /out/miniapp /usr/local/bin/miniapp
COPY --from=assets /deps/node_modules/@novnc/novnc/core ./vendor/core
COPY --from=assets /deps/node_modules/@novnc/novnc/vendor ./vendor/vendor
COPY tools/miniapp/web ./web
USER 65532:65532
ENTRYPOINT ["miniapp"]
