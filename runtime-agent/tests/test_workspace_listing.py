"""Проверяйте страницы рабочего каталога."""

import os

import pytest

from runtime_agent.workspace_listing import DirectoryChanged, list_directory


def test_pagination_and_revision(tmp_path):
    """Верните каталог первым и отклоните курсор после изменения."""
    (tmp_path / "z").mkdir()
    (tmp_path / "a").write_bytes(b"x")
    (tmp_path / "link").symlink_to("/tmp")
    root = os.open(tmp_path, os.O_DIRECTORY)
    try:
        first = list_directory(root, "", 1)
        assert [entry["name"] for entry in first["items"]] == ["z"]
        second = list_directory(root, "", 2, first["next_cursor"])
        assert [entry["type"] for entry in second["items"]] == ["file", "symlink"]
        (tmp_path / "b").touch()
        with pytest.raises(DirectoryChanged):
            list_directory(root, "", 2, first["next_cursor"])
    finally:
        os.close(root)


def test_signed_cursor_is_scoped_to_path_and_generation(tmp_path):
    """Курсор одной сессии не применяется к другой."""
    from runtime_agent.workspace_listing import list_workspace

    (tmp_path / "data").mkdir()
    for name in ("a", "b"):
        (tmp_path / "data" / name).touch()
    first = list_workspace(tmp_path, "generation", "secret", "", 1, None)
    assert first["next_cursor"]
    assert (
        list_workspace(tmp_path, "generation", "secret", "", 1, first["next_cursor"])["items"][0][
            "name"
        ]
        == "b"
    )
    with pytest.raises(DirectoryChanged):
        list_workspace(tmp_path, "other", "secret", "", 1, first["next_cursor"])
    with pytest.raises(ValueError):
        list_workspace(tmp_path, "generation", "wrong", "", 1, first["next_cursor"])


@pytest.mark.parametrize("cursor", ["!", "a", "====", " " * 20])
def test_malformed_cursor_returns_validation_error(tmp_path, cursor):
    """Отклоняйте повреждённые курсоры без обращения к дереву."""
    from runtime_agent.workspace_listing import list_workspace

    with pytest.raises(ValueError):
        list_workspace(tmp_path, "generation", "secret", "", 1, cursor)
