import json
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
RUNTIME = ROOT / "data" / "output" / "smoke-50000"


def _load(name: str) -> tuple[Path, dict]:
    path = ROOT / "config" / name
    return path, json.loads(path.read_text(encoding="utf-8"))


def test_host_examples_resolve_to_the_generated_runtime_artifacts():
    for name in ("workload.local.example.json", "workload.stand.example.json"):
        path, config = _load(name)
        query = (path.parent / config["query_file"]).resolve()
        documents = (path.parent / config["document_file"]).resolve()
        assert query == RUNTIME / "workload-queries.json.gz"
        assert documents == RUNTIME / "workload-documents.jsonl.gz"


def test_examples_do_not_contain_credentials():
    for name in ("workload.local.example.json", "workload.stand.example.json"):
        _, config = _load(name)
        assert "token" not in config
        assert "ca_file" not in config


def test_dataset_profiles_are_independent_and_share_the_selection_seed():
    catalog = json.loads((ROOT / "config" / "datasets.json").read_text(encoding="utf-8"))
    baseline = catalog["profiles"]["baseline-50k"]
    scale = catalog["profiles"]["scale-1m"]

    assert baseline["size"] == 50_000
    assert scale["size"] == 1_000_000
    assert baseline["seed"] == scale["seed"] == 20_260_825
    assert baseline["output_dir"] != scale["output_dir"]
    assert baseline["document_file"] != scale["document_file"]
