"""Загружайте и проверяйте настройки агента."""

import math
import os
from dataclasses import dataclass
from ipaddress import IPv4Address
from pathlib import Path

# Максимум одного временного файла в байтах по контракту #118.
MAX_WORKSPACE_FILE_BYTES = 3221225472


@dataclass(frozen=True)
class Settings:
    """Хранит параметры хоста и ограничения kernel.

    Назначение, язык и токен поступают от Go API и сохраняются в journal.
    """

    state_dir: Path
    kernel_network: str
    kernel_dns: str
    max_output_bytes: int = 10 * 1024 * 1024
    idle_seconds: float = 900
    # медленная VPS поднимает Jupyter дольше 30 с; после таймаута сессия FAILED без повтора
    startup_seconds: float = 120
    interrupt_seconds: float = 5
    monitor_seconds: float = 2
    python_image: str = "cellestial-kernel-python:local"
    workspace_path: Path | None = None

    def __post_init__(self):
        """Проверьте параметры сети, DNS и ресурсные лимиты."""
        IPv4Address(self.kernel_dns)
        if self.workspace_path is not None and not self.workspace_path.is_absolute():
            raise ValueError("WORKSPACE_PATH must be absolute")
        for name in (
            "max_output_bytes",
            "idle_seconds",
            "startup_seconds",
            "interrupt_seconds",
            "monitor_seconds",
        ):
            if not math.isfinite(getattr(self, name)) or getattr(self, name) <= 0:
                raise ValueError(f"{name} must be positive")
        if not self.kernel_network or self.kernel_network in ("bridge", "host", "none"):
            raise ValueError("KERNEL_NETWORK must name a dedicated bridge network")

    @classmethod
    def from_env(cls):
        """Прочитайте настройки из окружения и примените значения по умолчанию."""
        workspace_path = os.environ.get("WORKSPACE_PATH")
        return cls(
            state_dir=Path(os.environ.get("STATE_DIR", "/var/lib/cellestial-agent")).resolve(),
            kernel_network=os.environ.get("KERNEL_NETWORK", ""),
            kernel_dns=os.environ.get("KERNEL_DNS", ""),
            max_output_bytes=int(os.environ.get("MAX_OUTPUT_BYTES", str(10 * 1024 * 1024))),
            idle_seconds=float(os.environ.get("IDLE_TIMEOUT_SECONDS", "900")),
            python_image=os.environ.get("PYTHON_IMAGE", "cellestial-kernel-python:local"),
            workspace_path=Path(workspace_path) if workspace_path else None,
        )
