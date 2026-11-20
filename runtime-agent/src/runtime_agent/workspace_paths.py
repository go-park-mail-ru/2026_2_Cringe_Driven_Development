"""Проверяйте и открывайте пользовательские пути с ограничениями Linux openat2."""

import ctypes
import os
import platform

# Номер одинаков для архитектур runtime VPS и локального arm64.
_OPENAT2 = 437
_RESOLVE_NO_XDEV = 0x01
_RESOLVE_NO_SYMLINKS = 0x04
_RESOLVE_BENEATH = 0x08
_LIBC = ctypes.CDLL(None, use_errno=True)
_LIBC.syscall.restype = ctypes.c_long


class _OpenHow(ctypes.Structure):
    """Храните параметры системного вызова openat2."""

    _fields_ = [
        ("flags", ctypes.c_uint64),
        ("mode", ctypes.c_uint64),
        ("resolve", ctypes.c_uint64),
    ]


def validate_path(path: str) -> bytes:
    """Верните байты относительного UTF-8 пути. Пустая строка обозначает корень."""
    if not path:
        return b"."
    try:
        encoded = path.encode("utf-8", errors="strict")
    except UnicodeEncodeError as exc:
        raise ValueError("Invalid workspace path encoding") from exc
    if len(encoded) > 4095 or b"\0" in encoded:
        raise ValueError("Invalid workspace path")
    if any(part in (b"", b".", b"..") or len(part) > 255 for part in encoded.split(b"/")):
        raise ValueError("Invalid workspace path component")
    return encoded


def open_entry(root_fd: int, path: str, flags: int) -> int:
    """Откройте путь под root_fd без перехода по ссылкам и через mount.

    Вызывающий закрывает возвращённый descriptor. O_PATH | O_NOFOLLOW
    позволяет открыть саму ссылку для проверки её метаданных.
    """
    encoded = validate_path(path)
    if platform.system() != "Linux" or platform.machine() not in ("x86_64", "aarch64"):
        raise RuntimeError("Workspace lookup requires Linux openat2")
    if not flags & os.O_PATH:
        flags |= os.O_NONBLOCK
    how = _OpenHow(
        flags | os.O_CLOEXEC,
        0,
        _RESOLVE_BENEATH | _RESOLVE_NO_SYMLINKS | _RESOLVE_NO_XDEV,
    )
    descriptor = _LIBC.syscall(
        ctypes.c_long(_OPENAT2),
        ctypes.c_int(root_fd),
        ctypes.c_char_p(encoded),
        ctypes.byref(how),
        ctypes.c_size_t(ctypes.sizeof(how)),
    )
    if descriptor < 0:
        error = ctypes.get_errno()
        raise OSError(error, os.strerror(error))
    return descriptor
