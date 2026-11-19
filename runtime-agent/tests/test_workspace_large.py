"""Измеряйте реальные передачи 3 ГиБ с ограниченным буфером."""

import hashlib
import os

import pytest

from runtime_agent.workspace_transfers import CHUNK, MAX_FILE, FileError, Transfers


@pytest.mark.skipif(
    os.environ.get("CELL_TEST_LARGE") != "1", reason="Set CELL_TEST_LARGE=1 for 3 GiB IO"
)
def test_exact_three_gib_upload_commit_download(tmp_path):
    """Сверьте SHA-256 и inode после записи и чтения ровно 3221225472 байт."""
    (tmp_path / "data").mkdir()
    (tmp_path / "staging").mkdir()
    service = Transfers(tmp_path, tmp_path, "large-test")
    try:
        payload = bytes(range(256)) * (CHUNK // 256)
        digest = hashlib.sha256()
        item = service.begin("upload", "archive.bin", MAX_FILE)
        tid = item["transfer_id"]
        inode = (tmp_path / "staging" / tid).stat().st_ino
        for offset in range(0, MAX_FILE, CHUNK):
            digest.update(payload)
            assert service.chunk(tid, offset, payload)["offset"] == offset + CHUNK
        assert service.commit(tid)["sha256"] == digest.hexdigest()
        assert (tmp_path / "data/archive.bin").stat().st_ino == inode
        assert not list((tmp_path / "staging").iterdir())
        downloaded = service.begin("download", "archive.bin")["transfer_id"]
        service.open_download(downloaded)
        result = hashlib.sha256()
        for offset in range(0, MAX_FILE, CHUNK):
            chunk = service.read(downloaded, offset, CHUNK)
            assert len(chunk) == CHUNK
            result.update(chunk)
            service.acknowledge_download(downloaded, offset + len(chunk))
        assert result.hexdigest() == digest.hexdigest()
        service.finish_download(downloaded, True)
        with pytest.raises(FileError, match="file_too_large"):
            service.begin("upload", "oversize", MAX_FILE + 1)
    finally:
        service.close()
