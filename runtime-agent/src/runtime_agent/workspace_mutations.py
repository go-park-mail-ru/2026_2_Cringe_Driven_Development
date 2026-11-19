"""Выполняйте файловые мутации относительно дескриптора workspace."""

import os

from .workspace_listing import entry_version
from .workspace_paths import open_entry, validate_path


def make_directory(root_fd: int, path: str) -> None:
    """Создайте один каталог в существующем родителе без перехода по ссылкам.

    root_fd остаётся у вызывающего. Коллизии и отсутствие родителя
    возвращаются как стандартные ошибки файловой системы.
    """
    if not path:
        raise ValueError("Cannot create the workspace root")
    validate_path(path)
    parent, _, name = path.rpartition("/")
    fd = open_entry(root_fd, parent, os.O_RDONLY | os.O_DIRECTORY)
    try:
        os.mkdir(name, mode=0o770, dir_fd=fd)
        created = open_entry(fd, name, os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fchmod(created, 0o2770)
            os.fsync(created)
        finally:
            os.close(created)
        os.fsync(fd)
    finally:
        os.close(fd)


class EntryChanged(Exception):
    """Запись заменена или изменена после чтения каталога."""


def mutate_entry(
    root_fd, staging_fd, operation_id, path, expected_version, new_name=None, recursive=False
):
    """Изолируйте проверяемую запись перед переименованием или удалением.

    Перенос в приватный staging исключает удаление нового объекта по старому пути.
    При подмене между проверкой и переносом запись возвращается без перезаписи.
    Неоднозначное восстановление требует явного resync.
    """
    import stat

    from .workspace_transfers import FileError, rename_noreplace

    if not path:
        raise ValueError("Cannot mutate workspace root")
    validate_path(path)
    if new_name is not None and (not new_name or "/" in new_name):
        raise ValueError("Invalid destination name")
    if new_name is not None:
        validate_path(new_name)
    parent, _, name = path.rpartition("/")
    fd = open_entry(root_fd, parent, os.O_RDONLY | os.O_DIRECTORY)
    isolated = "operation-" + operation_id
    try:
        source = open_entry(fd, name, os.O_PATH | os.O_NOFOLLOW)
        try:
            before = os.fstat(source)
            if entry_version(before) != expected_version:
                raise EntryChanged
            if not (
                stat.S_ISREG(before.st_mode)
                or stat.S_ISDIR(before.st_mode)
                or stat.S_ISLNK(before.st_mode)
            ):
                raise FileError("permission_denied")
            if stat.S_ISDIR(before.st_mode) and not recursive and new_name is None:
                directory = open_entry(fd, name, os.O_RDONLY | os.O_DIRECTORY)
                try:
                    with os.scandir(directory) as entries:
                        if next(entries, None) is not None:
                            raise FileError("not_empty")
                finally:
                    os.close(directory)
            rename_noreplace(fd, name, staging_fd, isolated)
            after = os.stat(isolated, dir_fd=staging_fd, follow_symlinks=False)
            if (before.st_dev, before.st_ino, before.st_size, before.st_mtime_ns) != (
                after.st_dev,
                after.st_ino,
                after.st_size,
                after.st_mtime_ns,
            ):
                try:
                    rename_noreplace(staging_fd, isolated, fd, name)
                except OSError as exc:
                    raise FileError("operation_outcome_unknown", resync=True) from exc
                raise EntryChanged
            try:
                if new_name is not None:
                    rename_noreplace(staging_fd, isolated, fd, new_name)
                elif stat.S_ISDIR(after.st_mode):
                    if recursive:
                        _remove_tree(staging_fd, isolated)
                    else:
                        os.rmdir(isolated, dir_fd=staging_fd)
                else:
                    os.unlink(isolated, dir_fd=staging_fd)
            except OSError:
                try:
                    rename_noreplace(staging_fd, isolated, fd, name)
                except OSError as exc:
                    raise FileError("operation_outcome_unknown", resync=True) from exc
                raise
            os.fsync(fd)
            os.fsync(staging_fd)
        finally:
            os.close(source)
    finally:
        os.close(fd)


def _remove_tree(parent, name):
    """Удалите изолированное дерево, не следуя по симлинкам и другим mount."""
    import stat

    fd = open_entry(parent, name, os.O_RDONLY | os.O_DIRECTORY)
    try:
        with os.scandir(fd) as entries:
            for child in entries:
                if stat.S_ISDIR(child.stat(follow_symlinks=False).st_mode):
                    _remove_tree(fd, child.name)
                else:
                    os.unlink(child.name, dir_fd=fd)
        os.rmdir(name, dir_fd=parent)
    finally:
        os.close(fd)
