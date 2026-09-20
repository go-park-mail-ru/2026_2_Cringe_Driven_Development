FROM golang:1.27.1-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o server ./cmd/main

FROM alpine:3.23
RUN adduser -D -H -u 10001 app
WORKDIR /app
COPY --from=builder /app/server .
USER app
EXPOSE 8080
CMD ["./server"]
