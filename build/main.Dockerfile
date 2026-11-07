FROM python:3.13-slim-bookworm AS agent
WORKDIR /src
COPY runtime-agent/ ./
RUN pip wheel --no-cache-dir --no-deps --wheel-dir /out . \
    && cd /out \
    && sha256sum cellestial_agent-*.whl | cut -d' ' -f1 > "$(ls cellestial_agent-*.whl).sha256"

FROM golang:1.27.1-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o server ./cmd/main
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o runtime-smoke ./cmd/runtime-smoke

FROM alpine:3.23
ARG RUNTIME_PYTHON_IMAGE=cellestial-kernel-python:local
ARG RUNTIME_R_IMAGE=cellestial-kernel-r:local
ARG RUNTIME_AGENT_VERSION=dev
ENV RUNTIME_PYTHON_IMAGE=$RUNTIME_PYTHON_IMAGE \
    RUNTIME_R_IMAGE=$RUNTIME_R_IMAGE \
    RUNTIME_AGENT_VERSION=$RUNTIME_AGENT_VERSION \
    RUNTIME_WORK_DIR=/var/lib/cellestial/runtime
RUN apk add --no-cache python3 ansible-core openssh-client \
    && adduser -D -H -u 10001 app \
    && mkdir -p /var/lib/cellestial/runtime /app/deploy/runtime /app/agent \
    && chown -R app:app /var/lib/cellestial
WORKDIR /app
COPY --from=builder /app/server .
COPY --from=builder /app/runtime-smoke /app/runtime-smoke
COPY --from=agent /out/ /app/agent/
COPY runtime-agent/requirements.txt /app/agent/requirements.txt
COPY runtime-agent/deploy/cellestial-agent.service /app/agent/cellestial-agent.service
USER app
EXPOSE 8080
CMD ["./server"]
