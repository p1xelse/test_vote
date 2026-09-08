# Контекст сборки — корень репозитория: в образ нужен и backend, и frontend.
FROM golang:1.26-alpine AS build

WORKDIR /src

# Зависимости отдельным слоем: правка кода не заставляет качать модули заново.
COPY backend/go.mod backend/go.sum ./
RUN go mod download

COPY backend/ ./

# CGO не нужен, статический бинарь едет в scratch-подобный образ.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/service ./cmd/service

FROM alpine:3.21

RUN adduser -D -u 10001 vote

WORKDIR /app

COPY --from=build /out/service /app/service
COPY backend/configs /app/configs
COPY frontend /app/frontend

USER vote

EXPOSE 8080 8081

ENV CONFIG_PATH=/app/configs/config.local.toml \
    FRONTEND_DIR=/app/frontend

ENTRYPOINT ["/app/service"]
