"""Load and validate runtime-agent configuration."""

import math
import os
from dataclasses import dataclass
from ipaddress import IPv4Address
from pathlib import Path
from uuid import UUID


@dataclass(frozen=True)
class Settings:
    """Store immutable configuration for one assigned runtime.

    Attributes:
        token (str): Shared bearer token for the private API.
        state_dir (Path): Directory containing SQLite state and the process lock.
        session_id (str): Assigned session UUID.
        language (str): Assigned kernel language.
        kernel_network (str): Infra-provisioned IPv4 bridge network.
        kernel_dns (str): Infra-approved IPv4 DNS resolver.
        max_output_bytes (int): Serialized output budget per execution or unattributed
            generation.
        idle_seconds (float): Idle lifetime of a session, in seconds.
        startup_seconds (float): Jupyter startup and restart deadline, in seconds.
        interrupt_seconds (float): Time to wait for interrupt confirmation, in seconds.
        monitor_seconds (float): Interval between lifecycle checks, in seconds.
        python_image (str): Docker image used for Python sessions.
        r_image (str): Docker image used for R sessions.
    """

    token: str
    state_dir: Path
    session_id: str
    language: str
    kernel_network: str
    kernel_dns: str
    max_output_bytes: int = 10 * 1024 * 1024
    idle_seconds: float = 900
    # медленная VPS поднимает Jupyter дольше 30 с; после таймаута сессия FAILED без повтора
    startup_seconds: float = 120
    interrupt_seconds: float = 5
    monitor_seconds: float = 2
    python_image: str = "cellestial-kernel-python:local"
    r_image: str = "cellestial-kernel-r:local"

    def __post_init__(self):
        """Validate credentials, assignment and resource limits.

        Raises:
            ValueError: The token, assignment, network, DNS or a numeric limit is invalid.
        """
        if len(self.token) < 32 or not self.token.isascii():
            raise ValueError("RUNTIME_TOKEN must contain at least 32 ASCII characters")
        IPv4Address(self.kernel_dns)
        for name in (
            "max_output_bytes",
            "idle_seconds",
            "startup_seconds",
            "interrupt_seconds",
            "monitor_seconds",
        ):
            if not math.isfinite(getattr(self, name)) or getattr(self, name) <= 0:
                raise ValueError(f"{name} must be positive")
        object.__setattr__(self, "session_id", str(UUID(self.session_id)))
        if self.language not in ("python", "r"):
            raise ValueError("KERNEL_LANGUAGE must be python or r")
        if not self.kernel_network or self.kernel_network in ("bridge", "host", "none"):
            raise ValueError("KERNEL_NETWORK must name a dedicated bridge network")

    @classmethod
    def from_env(cls):
        """Read settings from environment variables and apply defaults.

        Returns:
            Settings: Validated assignment configuration with an absolute state path.

        Raises:
            ValueError: An environment value cannot be parsed or fails validation.
        """
        return cls(
            token=os.environ.get("RUNTIME_TOKEN", ""),
            state_dir=Path(os.environ.get("STATE_DIR", "/var/lib/cellestial-agent")).resolve(),
            session_id=os.environ.get("SESSION_ID", ""),
            language=os.environ.get("KERNEL_LANGUAGE", ""),
            kernel_network=os.environ.get("KERNEL_NETWORK", ""),
            kernel_dns=os.environ.get("KERNEL_DNS", ""),
            max_output_bytes=int(os.environ.get("MAX_OUTPUT_BYTES", str(10 * 1024 * 1024))),
            idle_seconds=float(os.environ.get("IDLE_TIMEOUT_SECONDS", "900")),
            python_image=os.environ.get("PYTHON_IMAGE", "cellestial-kernel-python:local"),
            r_image=os.environ.get("R_IMAGE", "cellestial-kernel-r:local"),
        )
