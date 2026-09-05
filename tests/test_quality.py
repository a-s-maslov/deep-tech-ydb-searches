from __future__ import annotations

import numpy as np

from deep_tech_ydb_searches.quality import _fixed_vectors


def test_fixed_vectors_preserves_float32_matrix() -> None:
    import pyarrow as pa

    expected = np.asarray([[1.0, 2.0], [3.0, 4.0]], dtype=np.float32)
    column = pa.chunked_array(
        [pa.array(expected.tolist(), type=pa.list_(pa.float32(), 2))]
    )
    actual = _fixed_vectors(column, 2)
    np.testing.assert_array_equal(actual, expected)
    assert actual.flags.c_contiguous
