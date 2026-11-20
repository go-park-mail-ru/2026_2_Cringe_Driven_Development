"""Проверяйте durable chunks, ограниченную память и публикацию файла."""

import hashlib
import os

import pytest

from runtime_agent.workspace_transfers import CHUNK, MAX_FILE, FileError, Transfers


@pytest.fixture
def transfers(tmp_path):
    """Откройте изолированное хранилище и закройте после сценария."""
    (tmp_path / "data").mkdir()
    (tmp_path / "staging").mkdir()
    service = Transfers(tmp_path, tmp_path, "generation")
    yield service
    service.close()


def test_upload_ack_replay_commit(transfers, tmp_path):
    """Повтор ACK не дописывает файл, commit переносит тот же inode."""
    body = b"\x00\xffbinary"
    item = transfers.begin("upload", "file.bin", len(body), hashlib.sha256(body).hexdigest())
    tid = item["transfer_id"]
    inode = os.stat(tmp_path / "staging" / tid).st_ino
    assert transfers.chunk(tid, 0, body)["offset"] == len(body)
    assert transfers.chunk(tid, 0, body)["offset"] == len(body)
    with pytest.raises(FileError, match="conflict"):
        transfers.chunk(tid, 0, b"different")
    completed = transfers.commit(tid)
    assert completed["state"] == "completed"
    assert (tmp_path / "data/file.bin").read_bytes() == body
    assert os.stat(tmp_path / "data/file.bin").st_ino == inode
    assert not (tmp_path / "staging" / tid).exists()
    assert transfers.commit(tid) == completed


def test_recover_truncates_unacknowledged_tail(tmp_path):
    """Restart отбрасывает байты сверх согласованного checkpoint."""
    (tmp_path / "data").mkdir()
    (tmp_path / "staging").mkdir()
    service = Transfers(tmp_path, tmp_path, "generation")
    tid = service.begin("upload", "file", 6)["transfer_id"]
    service.chunk(tid, 0, b"abc")
    service.close()
    with (tmp_path / "staging" / tid).open("ab") as output:
        output.write(b"bad")
    service = Transfers(tmp_path, tmp_path, "generation")
    try:
        assert service.get(tid)["offset"] == 3
        assert (tmp_path / "staging" / tid).read_bytes() == b"abc"
        service.chunk(tid, 3, b"def")
        service.commit(tid)
        assert (tmp_path / "data/file").read_bytes() == b"abcdef"
    finally:
        service.close()


def test_empty_checksum_cancel_and_limits(transfers, tmp_path):
    """Пустой файл публикуется, неверный digest очищается, размер ограничен."""
    tid = transfers.begin("upload", "empty", 0)["transfer_id"]
    assert transfers.commit(tid)["sha256"] == hashlib.sha256(b"").hexdigest()
    bad = transfers.begin("upload", "bad", 1, "0" * 64)["transfer_id"]
    transfers.chunk(bad, 0, b"x")
    with pytest.raises(FileError, match="checksum_mismatch"):
        transfers.commit(bad)
    assert not (tmp_path / "staging" / bad).exists()
    with pytest.raises(FileError, match="file_too_large"):
        transfers.begin("upload", "huge", MAX_FILE + 1)
    other = transfers.begin("upload", "cancel", 1)["transfer_id"]
    assert transfers.cancel(other)["state"] == "cancelled"
    assert transfers.cancel(other)["state"] == "cancelled"


def test_download_detects_change(transfers, tmp_path):
    """Descriptor сохраняет файл, изменение содержимого обрывает чтение."""
    target = tmp_path / "data/source"
    target.write_bytes(b"abcdef")
    item = transfers.begin("download", "source")
    tid = item["transfer_id"]
    transfers.open_download(tid)
    assert transfers.read(tid, 1, 3) == b"bcd"
    target.write_bytes(b"changed")
    with pytest.raises(FileError, match="file_changed"):
        transfers.read(tid, 4, 2)
    transfers.finish_download(tid, False)


def test_upload_conflicting_commit_preserves_staging(transfers, tmp_path):
    """Чужое назначение не перезаписывается commit."""
    tid = transfers.begin("upload", "file", 1)["transfer_id"]
    transfers.chunk(tid, 0, b"x")
    (tmp_path / "data/file").write_bytes(b"python")
    with pytest.raises(FileExistsError):
        transfers.commit(tid)
    assert (tmp_path / "data/file").read_bytes() == b"python"
    assert (tmp_path / "staging" / tid).read_bytes() == b"x"


def test_two_transfers_and_large_offsets(transfers, tmp_path):
    """Ограничьте параллелизм и читайте offset за 2 ГиБ без полного буфера."""
    path = tmp_path / "data/large"
    with path.open("wb") as stream:
        stream.seek(2**31 + 5)
        stream.write(b"tail")
    tid = transfers.begin("download", "large")["transfer_id"]
    transfers.open_download(tid)
    assert transfers.read(tid, 2**31 + 5, CHUNK) == b"tail"
    transfers.begin("upload", "other", 0)
    with pytest.raises(FileError, match="rate_limited"):
        transfers.begin("upload", "third", 0)


def test_commit_rejects_replaced_parent(transfers, tmp_path):
    """Commit не публикует файл в новый каталог по старому имени."""
    (tmp_path / "data/folder").mkdir()
    tid = transfers.begin("upload", "folder/file", 1)["transfer_id"]
    transfers.chunk(tid, 0, b"x")
    (tmp_path / "data/folder").rename(tmp_path / "data/old")
    (tmp_path / "data/folder").mkdir()
    with pytest.raises(FileError, match="conflict"):
        transfers.commit(tid)
    assert not (tmp_path / "data/folder/file").exists()


def test_fifo_download_fails_without_waiting(transfers, tmp_path):
    """Специальный файл не блокирует агент при скачивании."""
    os.mkfifo(tmp_path / "data/pipe")
    with pytest.raises(FileError, match="permission_denied"):
        transfers.begin("download", "pipe")


def test_completed_download_cannot_bypass_active_limit(transfers, tmp_path):
    """Повторное открытие download также занимает активный слот."""
    (tmp_path / "data/file").write_bytes(b"x")
    tid = transfers.begin("download", "file")["transfer_id"]
    transfers.open_download(tid)
    transfers.read(tid, 0, 1)
    transfers.finish_download(tid, True)
    transfers.begin("upload", "one", 0)
    transfers.begin("upload", "two", 0)
    with pytest.raises(FileError, match="rate_limited"):
        transfers.open_download(tid)


def test_python_file_larger_than_limit_is_rejected(transfers, tmp_path):
    """Предел размера применяется к файлам, созданным непосредственно Python."""
    with (tmp_path / "data/oversized").open("wb") as stream:
        stream.truncate(MAX_FILE + 1)
    with pytest.raises(FileError, match="file_too_large"):
        transfers.begin("download", "oversized")


def test_ttl_removes_staging_and_is_generation_scoped(transfers, tmp_path, monkeypatch):
    """Неактивный transfer очищается, другая generation не получает его metadata."""
    from runtime_agent.workspace_transfers import TTL

    item = transfers.begin("upload", "expired", 1)
    tid = item["transfer_id"]
    monkeypatch.setattr(
        "runtime_agent.workspace_transfers.time.time", lambda: item["updated"] + TTL + 1
    )
    transfers.expire()
    assert not (tmp_path / "staging" / tid).exists()
    assert transfers.get(tid)["state"] == "expired"
    monkeypatch.undo()
    transfers.generation = "other"
    with pytest.raises(FileError, match="not_found"):
        transfers.get(tid)


def test_failed_staging_creation_releases_transfer_slot(transfers, monkeypatch):
    """ENOSPC до первого chunk не оставляет активную передачу без staging."""
    import errno

    original = os.open

    def full(path, flags, *args, **kwargs):
        """Откажите только созданию staging inode."""
        if flags & os.O_CREAT and kwargs.get("dir_fd") == transfers.staging:
            raise OSError(errno.ENOSPC, "full")
        return original(path, flags, *args, **kwargs)

    monkeypatch.setattr("runtime_agent.workspace_transfers.os.open", full)
    with pytest.raises(OSError) as result:
        transfers.begin("upload", "file", 0)
    assert result.value.errno == errno.ENOSPC
    assert transfers.active_count() == 0


def test_status_never_observes_uncommitted_offset(transfers):
    """Status не выдаёт offset незавершённой SQLite-транзакции другой передачи."""
    import json
    from concurrent.futures import ThreadPoolExecutor
    from threading import Event

    item = transfers.begin("upload", "file", 1)
    tid = item["transfer_id"]
    entered = Event()

    def read_status():
        """Считайте status из отдельного HTTP worker."""
        entered.set()
        return transfers.get(tid)

    with ThreadPoolExecutor(max_workers=1) as executor:
        with transfers.lock:
            transfers.db.execute("BEGIN")
            uncommitted = {**item, "offset": 1}
            transfers.db.execute(
                "UPDATE transfers SET body=? WHERE id=?", (json.dumps(uncommitted), tid)
            )
            pending = executor.submit(read_status)
            assert entered.wait(timeout=1)
            # Читатель без lock увидел бы offset=1 на общей SQLite connection.
            try:
                early = pending.result(timeout=0.05)
            except TimeoutError:
                early = None
            transfers.db.rollback()
        result = pending.result(timeout=1)
    assert early is None and result["offset"] == 0


def test_download_status_counts_only_acknowledged_bytes(transfers, tmp_path):
    """Предварительное чтение не выдаётся за переданный браузеру прогресс."""
    (tmp_path / "data/file").write_bytes(b"abcdef")
    tid = transfers.begin("download", "file")["transfer_id"]
    transfers.open_download(tid)
    assert transfers.read(tid, 0, 3) == b"abc"
    assert transfers.get(tid)["offset"] == 0
    assert transfers.acknowledge_download(tid, 3)["offset"] == 3
    transfers.finish_download(tid, False)
