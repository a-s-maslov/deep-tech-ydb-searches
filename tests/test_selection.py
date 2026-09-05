from deep_tech_ydb_searches.miracl import (
    Document,
    document_text,
    select_documents,
    stable_score,
)


def corpus(size: int):
    for index in range(size):
        yield Document(str(index), f"title {index}", f"body {index}")


def test_required_documents_are_preserved_and_background_is_deterministic():
    required = {"2", "17", "90"}
    first, total = select_documents(corpus(100), required, 20, 42)
    second, _ = select_documents(corpus(100), required, 20, 42)
    assert total == 100
    assert required <= first.keys()
    assert first.keys() == second.keys()
    expected_background = sorted(
        (str(i) for i in range(100) if str(i) not in required),
        key=lambda docid: stable_score(docid, 42),
    )[:17]
    assert set(expected_background) <= first.keys()


def test_size_smaller_than_required_is_rejected():
    try:
        select_documents(corpus(10), {"1", "2", "3"}, 2, 1)
    except ValueError as error:
        assert "smaller" in str(error)
    else:
        raise AssertionError("ValueError expected")


def test_document_text_combines_fields_and_removes_stress_marks():
    assert document_text("Кари́бский кризис", "  Основной текст  ") == (
        "Карибский кризис\n\nОсновной текст"
    )
