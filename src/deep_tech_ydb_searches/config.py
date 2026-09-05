from __future__ import annotations

import json
from pathlib import Path
from typing import Any

from .paths import dataset_config_path, source_config_path


def load_sources(path: Path | None = None) -> dict[str, Any]:
    source = path or source_config_path()
    with source.open("r", encoding="utf-8") as stream:
        return json.load(stream)


def load_dataset_profile(name: str, path: Path | None = None) -> dict[str, Any]:
    source = path or dataset_config_path()
    with source.open("r", encoding="utf-8") as stream:
        profiles = json.load(stream).get("profiles", {})
    if name not in profiles:
        raise ValueError(f"unknown dataset profile: {name}")
    profile = dict(profiles[name])
    profile["name"] = name
    profile["output_dir"] = (source.parent / profile["output_dir"]).resolve()
    return profile
