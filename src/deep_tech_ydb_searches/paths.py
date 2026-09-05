from __future__ import annotations

import os
from pathlib import Path


def _environment_path(name: str) -> Path | None:
    value = os.environ.get(name)
    if not value:
        return None
    return Path(value).expanduser().resolve()


def project_root() -> Path:
    configured = _environment_path("DEEP_TECH_PROJECT_ROOT")
    if configured is not None:
        return configured

    # A regular (non-editable) package installation no longer points back to
    # the clone. Discover it from the process working directory instead.
    current = Path.cwd().resolve()
    for candidate in (current, *current.parents):
        if (candidate / "config" / "sources.json").is_file() and (
            candidate / "pyproject.toml"
        ).is_file():
            return candidate

    # Keep editable installs convenient even when invoked below the project.
    package_path = Path(__file__).resolve()
    for candidate in package_path.parents:
        if (candidate / "config" / "sources.json").is_file() and (
            candidate / "pyproject.toml"
        ).is_file():
            return candidate

    raise RuntimeError(
        "project root not found; run the command from the repository clone "
        "or set DEEP_TECH_PROJECT_ROOT"
    )


def data_root() -> Path:
    return _environment_path("DEEP_TECH_DATA") or project_root() / "data"


def source_config_path() -> Path:
    return (
        _environment_path("DEEP_TECH_SOURCE_CONFIG")
        or project_root() / "config" / "sources.json"
    )


def dataset_config_path() -> Path:
    return (
        _environment_path("DEEP_TECH_DATASET_CONFIG")
        or project_root() / "config" / "datasets.json"
    )
