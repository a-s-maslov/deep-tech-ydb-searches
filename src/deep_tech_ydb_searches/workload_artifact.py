from __future__ import annotations

import base64
import gzip
import json
import struct
from collections import defaultdict
from pathlib import Path

import pyarrow.parquet as pq

from .download import file_hash
from .lexical_query import build_keyword_query, build_lexical_query
from .miracl import document_text


def encode_float_vector(values: list[float]) -> bytes:
    return struct.pack(f"<{len(values)}fB", *values, 1)


def export_query_artifact(root: Path, query_output: Path) -> dict[str, object]:
    """Export the small query artifact without rewriting the document stream."""
    query_path = root / "queries-embedded.parquet"
    if not query_path.exists():
        raise FileNotFoundError(f"query vectors are missing: {query_path}")

    table = pq.read_table(query_path, columns=["qid", "query", "embedding"])
    qrels = pq.read_table(root / "qrels.parquet", columns=["qid", "docid", "relevance"])
    relevant_by_query: dict[str, list[str]] = defaultdict(list)
    for qid, docid, relevance in zip(
        qrels.column("qid").to_pylist(),
        qrels.column("docid").to_pylist(),
        qrels.column("relevance").to_pylist(),
        strict=True,
    ):
        if relevance > 0:
            relevant_by_query[qid].append(docid)
    data = table.to_pydict()
    queries = []
    for qid, text, vector in zip(
        data["qid"], data["query"], data["embedding"], strict=True
    ):
        relevant_docids = sorted(relevant_by_query.get(qid, []))
        lexical_query = build_lexical_query(text)
        queries.append(
            {
                "qid": qid,
                "text": text,
                "lexical_query": lexical_query,
                "fulltext_query": build_keyword_query(lexical_query) or lexical_query,
                "embedding": base64.b64encode(encode_float_vector(vector)).decode("ascii"),
                "relevant_docids": relevant_docids,
            }
        )
    if not queries:
        raise RuntimeError("prepared subset contains no embedded queries")

    payload = {
        "format_version": 2,
        "profile": root.name,
        "vector": {"dimension": 768, "metric": "inner_product"},
        "queries": queries,
    }
    query_output.parent.mkdir(parents=True, exist_ok=True)
    encoded = json.dumps(payload, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
    with query_output.open("wb") as raw:
        # mtime=0 makes repeated exports byte-for-byte reproducible.
        with gzip.GzipFile(fileobj=raw, mode="wb", mtime=0) as stream:
            stream.write(encoded)

    return {
        "query_path": str(query_output),
        "queries": len(queries),
        "query_bytes": query_output.stat().st_size,
        "query_sha256": file_hash(query_output),
    }


def export_document_artifact(root: Path, document_output: Path) -> dict[str, object]:
    """Export the initial-load and bounded-DML document stream."""

    document_output.parent.mkdir(parents=True, exist_ok=True)
    document_count = 0
    with document_output.open("wb") as raw:
        # Float vectors encoded as Base64 gain little from expensive level 9
        # compression. Level 1 keeps the artifact compact enough while making
        # million-row profile exports practical on a single-CPU bastion.
        with gzip.GzipFile(fileobj=raw, mode="wb", compresslevel=1, mtime=0) as stream:
            for path in sorted((root / "documents").glob("*.parquet")):
                parquet = pq.ParquetFile(path)
                for batch in parquet.iter_batches(batch_size=1000):
                    columns = batch.to_pydict()
                    # Older locally generated artifacts kept MIRACL ``body``.
                    # Accept them during the one-way format migration without
                    # requiring another 29 GB source-index extraction.
                    texts = columns.get("text")
                    if texts is None:
                        texts = [
                            document_text(title, body)
                            for title, body in zip(columns["title"], columns["body"], strict=True)
                        ]
                    for index in range(batch.num_rows):
                        row = {
                            "id": columns["id"][index],
                            "docid": columns["docid"][index],
                            "title": columns["title"][index],
                            "text": texts[index],
                            "embedding": base64.b64encode(
                                encode_float_vector(columns["embedding"][index])
                            ).decode("ascii"),
                        }
                        stream.write(
                            json.dumps(row, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
                            + b"\n"
                        )
                        document_count += 1
    return {
        "document_path": str(document_output),
        "documents": document_count,
        "document_bytes": document_output.stat().st_size,
        "document_sha256": file_hash(document_output),
    }


def export_workload_artifact(
    root: Path, query_output: Path, document_output: Path
) -> dict[str, object]:
    """Export query and document artifacts consumed by the Go binary.

    Parquet remains the canonical prepared format. The gzip query artifact is
    kept small for the long-running workload; the document stream is used for
    the initial import and as the bounded source pool for verified DML.
    """
    return {
        **export_query_artifact(root, query_output),
        **export_document_artifact(root, document_output),
    }


def write_runtime_checksums(query_path: Path, document_path: Path) -> Path:
    """Write portable checksums next to the two runtime artifacts."""
    if query_path.parent != document_path.parent:
        raise ValueError("runtime artifacts must share one directory")
    destination = query_path.parent / "SHA256SUMS"
    content = (
        f"{file_hash(document_path)}  {document_path.name}\n"
        f"{file_hash(query_path)}  {query_path.name}\n"
    )
    # write_bytes avoids Windows newline translation; Linux sha256sum treats
    # a stray CR as part of the file name.
    destination.write_bytes(content.encode("ascii"))
    return destination
