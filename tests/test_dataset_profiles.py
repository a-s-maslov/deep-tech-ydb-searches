import json

import pytest

from deep_tech_ydb_searches.config import load_dataset_profile


def test_load_dataset_profile_resolves_output_relative_to_catalog(tmp_path):
    catalog = tmp_path / "config" / "datasets.json"
    catalog.parent.mkdir()
    catalog.write_text(json.dumps({"profiles": {"scale-1m": {
        "size": 1_000_000,
        "seed": 42,
        "output_dir": "../data/output/scale-1m",
    }}}), encoding="utf-8")

    profile = load_dataset_profile("scale-1m", catalog)

    assert profile["size"] == 1_000_000
    assert profile["output_dir"] == tmp_path / "data" / "output" / "scale-1m"


def test_unknown_dataset_profile_is_rejected(tmp_path):
    catalog = tmp_path / "datasets.json"
    catalog.write_text('{"profiles": {}}', encoding="utf-8")
    with pytest.raises(ValueError, match="unknown dataset profile"):
        load_dataset_profile("missing", catalog)
