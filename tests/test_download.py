from pathlib import Path

import pytest

from deep_tech_ydb_searches import download


class _Response:
    def __init__(self, status: int, headers: dict[str, str]):
        self.status_code = status
        self.headers = headers

    def __enter__(self):
        return self

    def __exit__(self, *_):
        return False

    def raise_for_status(self):
        if self.status_code >= 400:
            raise RuntimeError(f"HTTP {self.status_code}")


def test_complete_cached_file_is_accepted_when_416_omits_content_range(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
):
    destination = tmp_path / "source.gz"
    destination.write_bytes(b"complete")
    responses = iter([
        _Response(416, {}),
        _Response(200, {"content-length": str(destination.stat().st_size)}),
    ])
    monkeypatch.setattr(download.requests, "get", lambda *args, **kwargs: next(responses))

    assert download.download_resumable("https://example.test/source", destination) == destination
    assert destination.read_bytes() == b"complete"


def test_oversized_cached_file_is_rejected_after_416_probe(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
):
    destination = tmp_path / "source.gz"
    destination.write_bytes(b"too-long")
    responses = iter([_Response(416, {}), _Response(200, {"content-length": "3"})])
    monkeypatch.setattr(download.requests, "get", lambda *args, **kwargs: next(responses))

    with pytest.raises(ValueError, match="larger than the remote source"):
        download.download_resumable("https://example.test/source", destination)
