from __future__ import annotations

import json
from pathlib import Path

import numpy as np
import pyarrow as pa
import pyarrow.parquet as pq

from .download import file_hash
from .miracl import Document, Qrel, document_text


def write_smoke(
    destination: Path,
    documents: dict[str, Document],
    positions: dict[str, int],
    vectors: dict[str, np.ndarray],
    topics: dict[str, str],
    qrels: list[Qrel],
    *,
    seed: int,
    corpus_rows: int,
    profile: str | None = None,
) -> None:
    destination.mkdir(parents=True, exist_ok=True)
    docs_dir = destination / "documents"
    docs_dir.mkdir(exist_ok=True)
    ordered_ids = sorted(documents, key=lambda docid: positions[docid])
    chunk_size = 5000
    document_files: list[Path] = []
    embedding_type = pa.list_(pa.float32(), 768)
    for shard, start in enumerate(range(0, len(ordered_ids), chunk_size)):
        ids = ordered_ids[start : start + chunk_size]
        table = pa.table(
            {
                "id": pa.array([positions[x] for x in ids], type=pa.uint64()),
                "docid": pa.array(ids, type=pa.string()),
                "title": pa.array([documents[x].title for x in ids], type=pa.string()),
                "text": pa.array(
                    [document_text(documents[x].title, documents[x].text) for x in ids],
                    type=pa.string(),
                ),
                "embedding": pa.array([vectors[x].tolist() for x in ids], type=embedding_type),
            }
        )
        path = docs_dir / f"part-{shard:05d}.parquet"
        pq.write_table(table, path, compression="zstd")
        document_files.append(path)

    query_table = pa.table(
        {
            "qid": pa.array(list(topics), type=pa.string()),
            "query": pa.array(list(topics.values()), type=pa.string()),
        }
    )
    pq.write_table(query_table, destination / "queries.parquet", compression="zstd")
    subset_ids = set(documents)
    kept_qrels = [qrel for qrel in qrels if qrel.docid in subset_ids]
    qrels_table = pa.table(
        {
            "qid": pa.array([x.qid for x in kept_qrels], type=pa.string()),
            "docid": pa.array([x.docid for x in kept_qrels], type=pa.string()),
            "relevance": pa.array([x.relevance for x in kept_qrels], type=pa.int32()),
        }
    )
    pq.write_table(qrels_table, destination / "qrels.parquet", compression="zstd")
    files = document_files + [destination / "queries.parquet", destination / "qrels.parquet"]
    manifest = {
        "format_version": 2,
        "profile": profile or f"smoke-{len(documents)}",
        "seed": seed,
        "corpus_rows_scanned": corpus_rows,
        "documents": len(documents),
        "queries": len(topics),
        "qrels": len(kept_qrels),
        "vector": {"dimension": 768, "dtype": "float32", "metric": "inner_product"},
        "document": {
            "fields": ["id", "docid", "title", "text", "embedding"],
            "text": "normalized(title + two newlines + MIRACL passage)",
        },
        "files": [
            {"path": str(path.relative_to(destination)), "size": path.stat().st_size, "sha256": file_hash(path)}
            for path in files
        ],
    }
    (destination / "manifest.json").write_text(
        json.dumps(manifest, ensure_ascii=False, indent=2), encoding="utf-8"
    )
