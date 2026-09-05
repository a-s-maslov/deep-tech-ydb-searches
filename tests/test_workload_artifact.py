import base64
import gzip
import json
from pathlib import Path

from deep_tech_ydb_searches.cli import cmd_make_fixture
from deep_tech_ydb_searches import cli
from deep_tech_ydb_searches.paths import data_root
from deep_tech_ydb_searches.workload_artifact import (
    export_query_artifact,
    export_workload_artifact,
    write_runtime_checksums,
)


class Args:
    size = 4


def test_export_workload_artifact(tmp_path, monkeypatch):
    monkeypatch.setenv("DEEP_TECH_DATA", str(tmp_path))
    cmd_make_fixture(Args())
    source = tmp_path / "output" / "smoke-4"
    assert data_root() == tmp_path.resolve()
    output = tmp_path / "queries.json.gz"
    documents = tmp_path / "documents.jsonl.gz"

    result = export_workload_artifact(source, output, documents)

    with gzip.open(output, "rt", encoding="utf-8") as stream:
        payload = json.load(stream)
    vector = base64.b64decode(payload["queries"][0]["embedding"])
    assert result["queries"] == 1
    assert result["documents"] == 4
    assert payload["format_version"] == 2
    assert payload["vector"] == {"dimension": 768, "metric": "inner_product"}
    assert payload["queries"][0]["relevant_docids"]
    assert payload["queries"][0]["lexical_query"] == "ошибка подключения"
    assert payload["queries"][0]["fulltext_query"] == "ошибка подключения"
    assert len(vector) == 768 * 4 + 1
    assert vector[-1] == 1
    with gzip.open(documents, "rt", encoding="utf-8") as stream:
        first_document = json.loads(stream.readline())
    assert set(first_document) == {"id", "docid", "title", "text", "embedding"}
    assert first_document["text"].startswith(first_document["title"] + "\n\n")

    checksums = write_runtime_checksums(output, documents)
    lines = checksums.read_text(encoding="ascii").splitlines()
    assert lines[0].endswith("  documents.jsonl.gz")
    assert lines[1].endswith("  queries.json.gz")
    assert b"\r" not in checksums.read_bytes()


def test_export_query_artifact_does_not_need_document_export(tmp_path, monkeypatch):
    monkeypatch.setenv("DEEP_TECH_DATA", str(tmp_path))
    cmd_make_fixture(Args())
    source = tmp_path / "output" / "smoke-4"
    output = tmp_path / "queries.json.gz"

    result = export_query_artifact(source, output)

    assert result["queries"] == 1
    assert output.exists()


def test_export_workload_command_resolves_named_profile(tmp_path, monkeypatch):
    output = tmp_path / "scale-1m"
    calls = []
    monkeypatch.setattr(
        cli,
        "load_dataset_profile",
        lambda name: {"name": name, "size": 1_000_000, "output_dir": output},
    )
    monkeypatch.setattr(
        "deep_tech_ydb_searches.workload_artifact.export_workload_artifact",
        lambda root, query, documents: calls.append((root, query, documents)) or {},
    )
    monkeypatch.setattr(
        "deep_tech_ydb_searches.workload_artifact.write_runtime_checksums",
        lambda query, documents: Path("SHA256SUMS"),
    )

    cli.cmd_export_workload(
        type("Args", (), {"profile": "scale-1m", "size": 1, "output": "", "output_dir": None})()
    )

    assert calls[0][0] == output
