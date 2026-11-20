"""Читайте страницы непосредственных детей каталога с ограниченной памятью."""

import hashlib
import heapq
import json
import os
import stat
from datetime import UTC, datetime

from .config import MAX_WORKSPACE_FILE_BYTES
from .workspace_paths import open_entry


class DirectoryChanged(Exception):
    """Каталог изменился, клиенту нужна новая первая страница."""


def entry_version(info):
    """Верните непрозрачную версию метаданных inode."""
    fields = (info.st_dev, info.st_ino, info.st_ctime_ns, info.st_mtime_ns, info.st_size)
    return hashlib.sha256(repr(fields).encode("ascii")).hexdigest()


def describe_entry(path, name, info, parent_fd=None):
    """Верните описание entry без перехода по симлинку."""
    mode = info.st_mode
    if stat.S_ISDIR(mode):
        kind, actions = "directory", ["list", "rename", "delete"]
    elif stat.S_ISREG(mode):
        kind, actions = "file", ["download", "rename", "delete"]
    elif stat.S_ISLNK(mode):
        kind, actions = "symlink", ["rename", "delete"]
    else:
        kind, actions = "unsupported", []
    if kind == "file" and info.st_size > MAX_WORKSPACE_FILE_BYTES:
        actions.remove("download")
    if parent_fd is not None:
        parent = os.fstat(parent_fd)
        if not os.access(".", os.W_OK | os.X_OK, dir_fd=parent_fd) or (
            parent.st_mode & stat.S_ISVTX and os.geteuid() not in (0, parent.st_uid, info.st_uid)
        ):
            actions = [action for action in actions if action not in ("rename", "delete")]
        if kind == "file" and not os.access(name, os.R_OK, dir_fd=parent_fd, follow_symlinks=False):
            actions = [action for action in actions if action != "download"]
        if kind == "directory" and not os.access(name, os.R_OK | os.X_OK, dir_fd=parent_fd):
            actions = [action for action in actions if action != "list"]
    return {
        "name": name,
        "path": f"{path}/{name}" if path else name,
        "type": kind,
        "size_bytes": info.st_size if kind == "file" else None,
        "modified_at": datetime.fromtimestamp(info.st_mtime, UTC).isoformat(),
        "version": entry_version(info),
        "actions": actions,
    }


def _entries(fd, path, after, digests):
    """Перечислите entries и учтите версии всей страницы и соседних страниц."""
    with os.scandir(fd) as children:
        for child in children:
            try:
                name = child.name.encode("utf-8")
                info = child.stat(follow_symlinks=False)
            except (UnicodeEncodeError, FileNotFoundError):
                continue
            entry = describe_entry(path, child.name, info, fd)
            kind = entry["type"]
            key = (int(kind != "directory"), name)
            version = entry_version(info)
            digest = hashlib.sha256(name + b"\0" + version.encode("ascii")).digest()
            digests[0] ^= int.from_bytes(digest)
            if after is None or key > after:
                yield key, entry


def list_directory(root_fd, path, limit=100, cursor=None):
    """Верните страницу каталога и внутренний курсор (revision, sort key)."""
    if not 1 <= limit <= 500:
        raise ValueError("Invalid directory page limit")
    fd = open_entry(root_fd, path, os.O_RDONLY | os.O_DIRECTORY)
    try:
        initial = entry_version(os.fstat(fd))
        digests = [0]
        after = cursor[1] if cursor is not None else None
        selected = heapq.nsmallest(
            limit + 1, _entries(fd, path, after, digests), key=lambda item: item[0]
        )
        revision = f"{initial}{digests[0]:064x}"
        if entry_version(os.fstat(fd)) != initial or (cursor is not None and cursor[0] != revision):
            raise DirectoryChanged
        page = []
        size = 0
        for key, entry in selected[:limit]:
            entry_size = len(json.dumps(entry, ensure_ascii=False).encode())
            if page and size + entry_size > 256 * 1024:
                break
            page.append((key, entry))
            size += entry_size
        return {
            "path": path,
            "items": [entry for _, entry in page],
            "next_cursor": (revision, page[-1][0]) if len(selected) > len(page) else None,
            "revision": revision,
        }
    finally:
        os.close(fd)


def list_workspace(root, generation, secret, path, limit, cursor):
    """Прочитайте страницу workspace и свяжите курсор с сессией и путём."""
    import base64
    import binascii
    import hmac
    import json

    def signature(payload):
        """Подпишите содержимое курсора ключом текущей сессии."""
        return hmac.digest(secret.encode(), payload, "sha256")

    position = None
    if cursor is not None:
        try:
            raw = base64.urlsafe_b64decode(cursor)
            mac, payload = raw[:32], raw[32:]
            if not hmac.compare_digest(mac, signature(payload)):
                raise ValueError("Invalid directory cursor")
            cursor_generation, cursor_path, revision, group, name = json.loads(payload)
            if (cursor_generation, cursor_path) != (generation, path):
                raise DirectoryChanged
            position = (revision, (group, name.encode()))
        except (binascii.Error, UnicodeError, TypeError) as exc:
            raise ValueError("Invalid directory cursor") from exc
    fd = os.open(root / "data", os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC)
    try:
        result = list_directory(fd, path, limit, position)
    finally:
        os.close(fd)
    if result["next_cursor"] is not None:
        revision, (group, name) = result["next_cursor"]
        payload = json.dumps([generation, path, revision, group, name.decode()]).encode()
        result["next_cursor"] = base64.urlsafe_b64encode(signature(payload) + payload).decode()
    result["workspace_generation"] = generation
    return result
