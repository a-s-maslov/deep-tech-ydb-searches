from __future__ import annotations

import gzip
import hashlib
import json
from pathlib import Path
from typing import Any

import numpy as np
import pyarrow.parquet as pq


def _sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def _fixed_vectors(column: Any, dimension: int) -> np.ndarray:
    """Convert an Arrow fixed-size-list column without Python row expansion."""
    combined = column.combine_chunks()
    values = combined.values.to_numpy(zero_copy_only=False)
    result = np.asarray(values, dtype=np.float32).reshape(len(combined), dimension)
    return np.ascontiguousarray(result)


def build_exact_artifact(root: Path, output: Path, *, top_k: int = 30) -> dict[str, Any]:
    if top_k < 1:
        raise ValueError("top_k must be positive")
    manifest_path = root / "manifest.json"
    query_path = root / "queries-embedded.parquet"
    document_paths = sorted((root / "documents").glob("part-*.parquet"))
    if not manifest_path.exists() or not query_path.exists() or not document_paths:
        raise RuntimeError(f"prepared dataset is incomplete under {root}")

    try:
        import faiss  # type: ignore[import-not-found]
    except ImportError as error:
        raise RuntimeError(
            "build-exact requires the quality extra: pip install -e '.[quality]'"
        ) from error

    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    dimension = int(manifest["vector"]["dimension"])
    metric = manifest["vector"]["metric"]
    if metric != "inner_product":
        raise RuntimeError(f"unsupported exact metric: {metric}")

    index = faiss.IndexFlatIP(dimension)
    docids: list[str] = []
    for document_path in document_paths:
        table = pq.read_table(document_path, columns=["docid", "embedding"])
        batch_ids = table.column("docid").to_pylist()
        vectors = _fixed_vectors(table.column("embedding"), dimension)
        if len(batch_ids) != len(vectors):
            raise RuntimeError(f"docid/vector mismatch in {document_path}")
        index.add(vectors)
        docids.extend(batch_ids)

    if index.ntotal != int(manifest["documents"]):
        raise RuntimeError(
            f"document count mismatch: index={index.ntotal}, manifest={manifest['documents']}"
        )

    query_table = pq.read_table(query_path, columns=["qid", "embedding"])
    qids = query_table.column("qid").to_pylist()
    queries = _fixed_vectors(query_table.column("embedding"), dimension)
    scores, positions = index.search(queries, top_k)

    rows = []
    for row, qid in enumerate(qids):
        if np.any(positions[row] < 0):
            raise RuntimeError(f"exact search returned fewer than {top_k} rows for {qid}")
        rows.append(
            {
                "qid": qid,
                "docids": [docids[int(position)] for position in positions[row]],
                "scores": [float(value) for value in scores[row]],
            }
        )

    artifact = {
        "format_version": 1,
        "profile": manifest["profile"],
        "top_k": top_k,
        "metric": metric,
        "documents": index.ntotal,
        "queries": len(rows),
        "dataset_manifest_sha256": _sha256(manifest_path),
        "query_artifact_sha256": _sha256(query_path),
        "results": rows,
    }
    output.parent.mkdir(parents=True, exist_ok=True)
    temporary = output.with_name(f"{output.name}.tmp")
    with gzip.open(temporary, "wt", encoding="utf-8", compresslevel=9) as stream:
        json.dump(artifact, stream, ensure_ascii=False, separators=(",", ":"))
    temporary.replace(output)
    return {
        "output": str(output),
        "profile": artifact["profile"],
        "documents": artifact["documents"],
        "queries": artifact["queries"],
        "top_k": top_k,
        "sha256": _sha256(output),
    }
