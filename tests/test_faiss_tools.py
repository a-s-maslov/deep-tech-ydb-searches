from pathlib import Path

import numpy as np

from deep_tech_ydb_searches.faiss_tools import extract_vectors, find_positions


def test_selected_vectors_are_read_from_flat_index_without_loading_it(tmp_path: Path):
    docids = tmp_path / "docid"
    docids.write_text("a\nb\nc\n", encoding="utf-8")
    positions, total = find_positions(docids, {"a", "c"})
    assert positions == {"a": 0, "c": 2}
    assert total == 3

    matrix = np.arange(3 * 4, dtype="<f4").reshape(3, 4)
    index = tmp_path / "index"
    index.write_bytes(b"small-index-header" + matrix.tobytes())
    vectors = extract_vectors(index, positions, total, dimension=4)

    np.testing.assert_array_equal(vectors["a"], matrix[0])
    np.testing.assert_array_equal(vectors["c"], matrix[2])
