import pytest

from deep_tech_ydb_searches.lexical_query import build_keyword_query, build_lexical_query


def test_question_scaffolding_is_removed_but_meaning_is_kept():
    assert (
        build_lexical_query("В каком году Русь свергла татаро-монгольское иго?")
        == "Русь свергла татаро монгольское иго"
    )


def test_accents_are_normalized_and_numbers_are_preserved():
    assert (
        build_lexical_query("Когда «Аполло́н-15» прилетел в 1971 году?")
        == "Аполлон 15 прилетел 1971"
    )


def test_negation_is_not_removed():
    assert build_lexical_query("Почему индекс не работает?") == "индекс не работает"


def test_short_query_does_not_become_empty():
    assert build_lexical_query("Кто это?") == "Кто это"


def test_single_content_term_does_not_restore_question_scaffolding():
    assert build_lexical_query("Что такое эритроциты?") == "эритроциты"


def test_capitalized_name_is_not_mistaken_for_a_particle():
    assert build_lexical_query("Когда умер Ли Харви Освальд?") == "умер Ли Харви Освальд"


def test_capitalized_preposition_and_definition_scaffolding_are_removed():
    assert build_lexical_query("Что такое сериал «Во все тяжкие»?") == "сериал все тяжкие"


def test_keyword_query_removes_generic_intent():
    assert (
        build_keyword_query("начался Кари́бский кризис")
        == "Карибский кризис"
    )


def test_keyword_query_keeps_informative_source_terms_only():
    assert (
        build_keyword_query("медалей выиграл СССР Олимпийских играх 1972")
        == "СССР 1972"
    )


def test_keyword_query_deduplicates_terms_and_ordinals():
    assert build_keyword_query("первый СССР СССР") == "СССР"


def test_keyword_query_rejects_invalid_limit():
    with pytest.raises(ValueError):
        build_keyword_query("пример", max_terms=0)
