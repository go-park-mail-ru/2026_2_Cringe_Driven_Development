"""Load and validate runtime-agent configuration."""

import math
import os
from dataclasses import dataclass
from ipaddress import IPv4Address
from pathlib import Path


@dataclass(frozen=True)
class Settings:
    """Store immutable host configuration of the runtime agent.

    The session, its language and the API token are not configured here: Go API
    assigns them once with POST /assign and the journal keeps them.

    Attributes:
        state_dir (Path): Directory containing SQLite state and the process lock.
        kernel_network (str): Infra-provisioned IPv4 bridge network.
        kernel_dns (str): Infra-approved IPv4 DNS resolver.
        max_output_bytes (int): Serialized output budget per execution or unattributed
            generation.
        idle_seconds (float): Idle lifetime of a session, in seconds.
        startup_seconds (float): Jupyter startup and restart deadline, in seconds.
        interrupt_seconds (float): Time to wait for interrupt confirmation, in seconds.
        monitor_seconds (float): Interval between lifecycle checks, in seconds.
        python_image (str): Docker image of the Python kernel.
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

    def __post_init__(self):
        """Validate the network, DNS and resource limits.

        Raises:
            ValueError: The network, DNS or a numeric limit is invalid.
        """
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
        if not self.kernel_network or self.kernel_network in ("bridge", "host", "none"):
            raise ValueError("KERNEL_NETWORK must name a dedicated bridge network")

    @classmethod
    def from_env(cls):
        """Read settings from environment variables and apply defaults.

        Returns:
            Settings: Validated host configuration with an absolute state path.

        Raises:
            ValueError: An environment value cannot be parsed or fails validation.
        """
        return cls(
            state_dir=Path(os.environ.get("STATE_DIR", "/var/lib/cellestial-agent")).resolve(),
            kernel_network=os.environ.get("KERNEL_NETWORK", ""),
            kernel_dns=os.environ.get("KERNEL_DNS", ""),
            max_output_bytes=int(os.environ.get("MAX_OUTPUT_BYTES", str(10 * 1024 * 1024))),
            idle_seconds=float(os.environ.get("IDLE_TIMEOUT_SECONDS", "900")),
            python_image=os.environ.get("PYTHON_IMAGE", "cellestial-kernel-python:local"),
        )
