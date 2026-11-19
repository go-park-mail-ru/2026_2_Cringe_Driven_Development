"""Запускайте агент исполнения одним процессом Uvicorn."""

import os

import uvicorn

from .observability import configure_logging


def main():
    """Установите закрытую маску файлов и запустите HTTP-сервер."""
    os.umask(0o077)
    level = os.environ.get("LOG_LEVEL", "info")
    configure_logging(level)
    uvicorn.run(
        "runtime_agent.api:create_app",
        factory=True,
        host=os.environ.get("HOST", "127.0.0.1"),
        port=int(os.environ.get("PORT", "8000")),
        workers=1,
        ws_max_size=1024 * 1024 + 28,
        ws_max_queue=4,
        timeout_graceful_shutdown=45,
        proxy_headers=False,
        access_log=False,
        log_level=level,
    )


if __name__ == "__main__":
    main()
