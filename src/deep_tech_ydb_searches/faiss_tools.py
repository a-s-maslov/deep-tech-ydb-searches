from __future__ import annotations

from pathlib import Path

import numpy as np


def locate_index(root: Path) -> tuple[Path, Path]:
    indexes = list(root.rglob("index"))
    docids = list(root.rglob("docid"))
    if len(indexes) != 1 or len(docids) != 1:
        raise RuntimeError(f"expected one index and one docid under {root}")
    return indexes[0], docids[0]


def find_positions(docid_path: Path, selected_ids: set[str]) -> tuple[dict[str, int], int]:
    positions: dict[str, int] = {}
    total_rows = 0
    with docid_path.open("r", encoding="utf-8") as stream:
        for position, line in enumerate(stream):
            total_rows = position + 1
            docid = line.rstrip("\r\n")
            if docid in selected_ids:
                positions[docid] = position
    missing = selected_ids - positions.keys()
    if missing:
        raise RuntimeError(f"{len(missing)} selected docids are absent from FAISS map")
    return positions, total_rows


def extract_vectors(
    index_path: Path, positions: dict[str, int], total_rows: int, dimension: int = 768
) -> dict[str, np.ndarray]:
    """Read selected vectors without loading the complete IndexFlatIP.

    FAISS serializes the contiguous float32 matrix at the end of an IndexFlat
    file.  The public MIRACL index is about 29 GB uncompressed, while a smoke
    subset needs only a few hundred MB.  Mapping the final matrix keeps the
    preparation step usable on an ordinary workstation and does not alter any
    vector values.
    """
    payload_size = total_rows * dimension * np.dtype("<f4").itemsize
    header_size = index_path.stat().st_size - payload_size
    if not 0 < header_size <= 1 << 20:
        raise RuntimeError(
            f"unexpected IndexFlat layout: file={index_path.stat().st_size}, "
            f"rows={total_rows}, dimension={dimension}, header={header_size}"
        )
    ordered = sorted(positions.items(), key=lambda item: item[1])
    row_ids = np.asarray([position for _, position in ordered], dtype=np.int64)
    mapped = np.memmap(
        index_path,
        dtype="<f4",
        mode="r",
        offset=header_size,
        shape=(total_rows, dimension),
    )
    matrix = np.asarray(mapped[row_ids], dtype=np.float32).copy()
    del mapped
    return {docid: matrix[offset] for offset, (docid, _) in enumerate(ordered)}
