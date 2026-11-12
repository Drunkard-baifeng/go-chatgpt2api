FROM node:22.23.5-alpine AS frontend
WORKDIR /src/new_frontend
COPY new_frontend/package.json new_frontend/package-lock.json new_frontend/.npmrc ./
RUN npm ci --registry=https://registry.npmjs.org
COPY new_frontend/ ./
ENV VITE_API_URL=""
RUN npm run build

FROM golang:1.27.4-alpine AS build
WORKDIR /src
COPY go_backend/go.mod go_backend/go.sum ./
RUN go mod download
COPY go_backend/ ./
COPY --from=frontend /src/new_frontend/dist ./internal/app/console/dist
RUN CGO_ENABLED=0 GOWORK=off go build -trimpath -ldflags="-s -w" -o /out/chatgpt2api ./cmd/chatgpt2api

FROM alpine:3.23
RUN apk add --no-cache ca-certificates postgresql18-client
ENV PATH="/usr/libexec/postgresql18:${PATH}"
WORKDIR /app
RUN mkdir -p /app/data/images /app/data/image_tasks /app/data/files && chown -R 10001:10001 /app/data
COPY --from=build /out/chatgpt2api /app/chatgpt2api
COPY --from=build /src/internal/images/codec/LICENSE.* /src/internal/images/codec/NOTICE.* /app/licenses/webp/
COPY LICENSE NOTICE /app/licenses/
COPY new_frontend/THIRD_PARTY_NOTICES.md /app/licenses/console/
USER 10001:10001
EXPOSE 8100
ENTRYPOINT ["/app/chatgpt2api"]
CMD ["serve", "--config", "/app/config.yaml"]
