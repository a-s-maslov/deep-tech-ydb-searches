from deep_tech_ydb_searches.paths import data_root, project_root, source_config_path


def test_paths_can_be_configured_outside_editable_install(tmp_path, monkeypatch):
    root = tmp_path / "clone"
    config = root / "config" / "sources.json"
    config.parent.mkdir(parents=True)
    config.write_text("{}", encoding="utf-8")
    (root / "pyproject.toml").write_text("[project]\nname='fixture'\n", encoding="utf-8")

    working_directory = root / "scripts" / "nested"
    working_directory.mkdir(parents=True)
    monkeypatch.chdir(working_directory)
    monkeypatch.delenv("DEEP_TECH_PROJECT_ROOT", raising=False)
    monkeypatch.delenv("DEEP_TECH_DATA", raising=False)
    monkeypatch.delenv("DEEP_TECH_SOURCE_CONFIG", raising=False)

    assert project_root() == root
    assert data_root() == root / "data"
    assert source_config_path() == config


def test_explicit_paths_override_discovery(tmp_path, monkeypatch):
    root = tmp_path / "root"
    data = tmp_path / "runtime-data"
    config = tmp_path / "custom-sources.json"
    monkeypatch.setenv("DEEP_TECH_PROJECT_ROOT", str(root))
    monkeypatch.setenv("DEEP_TECH_DATA", str(data))
    monkeypatch.setenv("DEEP_TECH_SOURCE_CONFIG", str(config))

    assert project_root() == root.resolve()
    assert data_root() == data.resolve()
    assert source_config_path() == config.resolve()
