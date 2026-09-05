from __future__ import annotations

import json
from pathlib import Path

import pyarrow.parquet as pq

from .download import file_hash


def validate_smoke(root: Path, require_query_vectors: bool = True) -> dict[str, int]:
    manifest = json.loads((root / "manifest.json").read_text(encoding="utf-8"))
    if manifest.get("format_version") != 2:
        raise RuntimeError("unsupported prepared-dataset format; rebuild the profile")
    for item in manifest["files"]:
        path = root / item["path"]
        if not path.exists() or path.stat().st_size != item["size"]:
            raise RuntimeError(f"missing or truncated artifact: {path}")
        if file_hash(path) != item["sha256"]:
            raise RuntimeError(f"checksum mismatch: {path}")
    docids: set[str] = set()
    rows = 0
    for path in sorted((root / "documents").glob("*.parquet")):
        table = pq.read_table(path, columns=["id", "docid", "title", "text", "embedding"])
        rows += table.num_rows
        docids.update(table.column("docid").to_pylist())
        if table.schema.field("embedding").type.list_size != 768:
            raise RuntimeError(f"wrong vector dimension in {path}")
        if any(not value for value in table.column("text").to_pylist()):
            raise RuntimeError(f"empty document text in {path}")
    if rows != manifest["documents"] or len(docids) != rows:
        raise RuntimeError("document count or uniqueness mismatch")
    qrels = pq.read_table(root / "qrels.parquet")
    if not set(qrels.column("docid").to_pylist()) <= docids:
        raise RuntimeError("qrels reference documents outside the subset")
    query_path = root / ("queries-embedded.parquet" if require_query_vectors else "queries.parquet")
    queries = pq.read_table(query_path)
    if require_query_vectors and queries.schema.field("embedding").type.list_size != 768:
        raise RuntimeError("wrong query-vector dimension")
    return {"documents": rows, "queries": queries.num_rows, "qrels": qrels.num_rows}
