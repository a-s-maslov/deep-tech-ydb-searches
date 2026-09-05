import argparse
import json

from deep_tech_ydb_searches import cli


def test_prepare_runtime_runs_the_single_pipeline_in_order(monkeypatch):
    calls = []

    for name in (
        "cmd_download_index",
        "cmd_unpack_index",
        "cmd_download_miracl",
        "cmd_build_smoke",
        "cmd_encode_queries",
        "cmd_validate",
        "cmd_export_workload",
    ):
        monkeypatch.setattr(
            cli,
            name,
            lambda args, step=name: calls.append((step, vars(args))),
        )

    cli.cmd_prepare_runtime(
        argparse.Namespace(
            profile="",
            size=50000,
            seed=20260825,
            download_workers=12,
            query_batch_size=32,
        )
    )

    assert [name for name, _ in calls] == [
        "cmd_download_index",
        "cmd_unpack_index",
        "cmd_download_miracl",
        "cmd_build_smoke",
        "cmd_encode_queries",
        "cmd_validate",
        "cmd_export_workload",
    ]
    assert calls[0][1] == {"workers": 12}
    assert calls[3][1] == {
        "size": 50000,
        "output_dir": None,
        "seed": 20260825,
        "dataset_profile": None,
    }
    assert calls[4][1] == {"size": 50000, "output_dir": None, "batch_size": 32}


def test_prepare_runtime_resolves_named_profile(monkeypatch, tmp_path):
    calls = []
    monkeypatch.setattr(
        cli,
        "load_dataset_profile",
        lambda name: {
            "name": name,
            "size": 1_000_000,
            "seed": 20260825,
            "output_dir": tmp_path / "scale-1m",
        },
    )
    for name in (
        "cmd_download_index", "cmd_unpack_index", "cmd_download_miracl",
        "cmd_build_smoke", "cmd_encode_queries", "cmd_validate", "cmd_export_workload",
    ):
        monkeypatch.setattr(cli, name, lambda args, step=name: calls.append((step, vars(args))))

    cli.cmd_prepare_runtime(argparse.Namespace(
        profile="scale-1m", size=1, seed=1, download_workers=8, query_batch_size=64
    ))

    assert calls[3][1]["size"] == 1_000_000
    assert calls[3][1]["dataset_profile"] == "scale-1m"
    assert calls[3][1]["output_dir"] == tmp_path / "scale-1m"


def test_unpack_index_reuses_matching_verified_cache(tmp_path, monkeypatch):
    source = {"filename": "index.tar.gz", "size": 123, "md5": "abc"}
    destination = tmp_path / "work" / "castorini"
    destination.mkdir(parents=True)
    (destination / ".unpacked-source.json").write_text(
        json.dumps(source), encoding="utf-8"
    )
    located = (destination / "index", destination / "docid")
    calls = []

    monkeypatch.setattr(cli, "load_sources", lambda: {"castorini_index": source})
    monkeypatch.setattr(cli, "data_root", lambda: tmp_path)
    monkeypatch.setattr(cli, "locate_index", lambda root: located)
    monkeypatch.setattr(cli, "_safe_unpack", lambda *args: calls.append(args))

    cli.cmd_unpack_index(argparse.Namespace())

    assert calls == []
