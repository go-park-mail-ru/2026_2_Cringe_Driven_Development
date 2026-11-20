"""Проверяйте ограничения имён и открытие файлов внутри workspace."""

import os

import pytest

from runtime_agent.workspace_paths import open_entry, validate_path


@pytest.mark.parametrize(
    "path", ["/x", "../x", "x/../y", "x//y", "./x", "x/", "x\0y", "\ud800", "я" * 128]
)
def test_invalid_path(path):
    """Отклоняйте недопустимые пути до обращения к ФС."""
    with pytest.raises(ValueError):
        validate_path(path)


def test_open_entry_confines_lookup(tmp_path):
    """Сохраняйте Unicode в пути и блокируйте traversal через ссылку."""
    root = tmp_path / "data"
    root.mkdir()
    (root / "файл 1").write_bytes(b"ok")
    (root / "link").symlink_to(tmp_path)
    root_fd = os.open(root, os.O_DIRECTORY)
    try:
        fd = open_entry(root_fd, "файл 1", os.O_RDONLY)
        with os.fdopen(fd, "rb") as entry:
            assert entry.read() == b"ok"
        with pytest.raises(OSError):
            open_entry(root_fd, "link/data", os.O_RDONLY)
        fd = open_entry(root_fd, "link", os.O_PATH | os.O_NOFOLLOW)
        os.close(fd)
    finally:
        os.close(root_fd)


def test_mkdir_is_single_level_and_never_follows_links(tmp_path):
    """Создайте один каталог и сохраните существующие записи."""
    from runtime_agent.workspace_mutations import make_directory

    (tmp_path / "parent").mkdir()
    (tmp_path / "link").symlink_to("parent", target_is_directory=True)
    root = os.open(tmp_path, os.O_DIRECTORY)
    try:
        make_directory(root, "parent/new")
        assert (tmp_path / "parent/new").is_dir()
        with pytest.raises(FileExistsError):
            make_directory(root, "parent/new")
        with pytest.raises(FileNotFoundError):
            make_directory(root, "absent/new")
        with pytest.raises(OSError):
            make_directory(root, "link/new2")
        assert not (tmp_path / "parent/new2").exists()
    finally:
        os.close(root)


def test_rename_checks_version_and_does_not_replace(tmp_path):
    """Изолируйте источник и сохраните коллизию назначения."""
    from uuid import uuid4

    from runtime_agent.workspace_listing import list_directory
    from runtime_agent.workspace_mutations import EntryChanged, mutate_entry

    root_path = tmp_path / "data"
    root_path.mkdir()
    stage_path = tmp_path / "staging"
    stage_path.mkdir()
    (root_path / "source").write_text("source")
    (root_path / "target").write_text("target")
    root = os.open(root_path, os.O_DIRECTORY)
    stage = os.open(stage_path, os.O_DIRECTORY)
    try:
        version = list_directory(root, "")["items"][0]["version"]
        with pytest.raises(FileExistsError):
            mutate_entry(root, stage, str(uuid4()), "source", version, "target")
        assert (root_path / "source").read_text() == "source"
        assert (root_path / "target").read_text() == "target"
        with pytest.raises(EntryChanged):
            mutate_entry(root, stage, str(uuid4()), "source", version, "new")
        version = list_directory(root, "")["items"][0]["version"]
        mutate_entry(root, stage, str(uuid4()), "source", version, "new")
        assert (root_path / "new").read_text() == "source"
        assert not (root_path / "source").exists()
    finally:
        os.close(root)
        os.close(stage)


def test_delete_race_preserves_replacement(tmp_path, monkeypatch):
    """Подмена перед изоляцией не удаляет новый объект по прежнему пути."""
    from uuid import uuid4

    from runtime_agent.workspace_listing import entry_version
    from runtime_agent.workspace_mutations import EntryChanged, mutate_entry
    from runtime_agent.workspace_transfers import rename_noreplace

    data = tmp_path / "data"
    staging = tmp_path / "staging"
    data.mkdir()
    staging.mkdir()
    (data / "source").write_text("old")
    version = entry_version((data / "source").stat())
    root = os.open(data, os.O_DIRECTORY)
    stage = os.open(staging, os.O_DIRECTORY)
    swapped = False

    def swap(source_fd, source, target_fd, target):
        """Подмените исходный путь непосредственно перед первым renameat2."""
        nonlocal swapped
        if not swapped:
            swapped = True
            (data / "source").rename(data / "original")
            (data / "source").write_text("replacement")
        return rename_noreplace(source_fd, source, target_fd, target)

    monkeypatch.setattr("runtime_agent.workspace_transfers.rename_noreplace", swap)
    try:
        with pytest.raises(EntryChanged):
            mutate_entry(root, stage, str(uuid4()), "source", version)
        assert (data / "source").read_text() == "replacement"
        assert (data / "original").read_text() == "old"
        assert not list(staging.iterdir())
    finally:
        os.close(root)
        os.close(stage)
