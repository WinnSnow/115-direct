FROM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
COPY internal/httpapi/ui/ /src/internal/httpapi/ui/
RUN npm run build

FROM golang:1.24-alpine AS builder
WORKDIR /src
RUN apk add --no-cache ca-certificates git
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/internal/httpapi/ui/ ./internal/httpapi/ui/
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/115-direct ./cmd/115-direct

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata su-exec && addgroup -g 1000 app && adduser -D -u 1000 -G app app
WORKDIR /app
COPY --from=builder /out/115-direct /usr/local/bin/115-direct
COPY deploy/docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod +x /usr/local/bin/docker-entrypoint.sh && mkdir -p /data/strm && chown -R app:app /data
ENV DATA_DIR=/data TZ=Asia/Shanghai
VOLUME ["/data"]
EXPOSE 9527 9096 9528
ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
