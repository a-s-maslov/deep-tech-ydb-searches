from __future__ import annotations

import re
import unicodedata


# This is deliberately a query-side list.  We do not remove these terms from
# the document index: doing so would make exact phrases and future query modes
# impossible.  The list contains only Russian question scaffolding and very
# common function words which are especially expensive with OR/MSM queries.
_RU_QUERY_STOP_WORDS = frozenset(
    {
        "а",
        "без",
        "был",
        "была",
        "были",
        "было",
        "быть",
        "в",
        "во",
        "где",
        "год",
        "года",
        "году",
        "годы",
        "да",
        "для",
        "до",
        "за",
        "зачем",
        "и",
        "из",
        "или",
        "к",
        "как",
        "какая",
        "какие",
        "каким",
        "какими",
        "каких",
        "какого",
        "какое",
        "какой",
        "каком",
        "какому",
        "какую",
        "когда",
        "кем",
        "ко",
        "кого",
        "кто",
        "куда",
        "лет",
        "ли",
        "между",
        "можно",
        "на",
        "над",
        "о",
        "об",
        "обо",
        "от",
        "откуда",
        "по",
        "под",
        "почему",
        "при",
        "про",
        "с",
        "сколько",
        "скольких",
        "скольким",
        "сколькими",
        "со",
        "такая",
        "такие",
        "таким",
        "таких",
        "такое",
        "такой",
        "у",
        "чего",
        "чем",
        "чему",
        "через",
        "что",
        "это",
        "является",
        "являются",
    }
)

_TOKEN = re.compile(r"[^\W_]+", re.UNICODE)

# Generic intent verbs are useful in a natural-language question but make a
# poor standalone keyword workload. They are removed only from the synthetic
# full-text load representation, never from the quality input or document
# index.
_RU_KEYWORD_SCAFFOLDING = frozenset(
    {
        "был",
        "была",
        "были",
        "было",
        "впервые",
        "возле",
        "вернулся",
        "выиграл",
        "выйграл",
        "закончил",
        "закончилась",
        "закончился",
        "звали",
        "зовут",
        "излечиться",
        "исполнена",
        "исполнил",
        "исполнила",
        "книгу",
        "называется",
        "называлась",
        "назывался",
        "началась",
        "начался",
        "начали",
        "начинается",
        "написал",
        "написала",
        "находится",
        "означает",
        "основан",
        "основана",
        "основано",
        "первоначально",
        "первое",
        "первого",
        "первой",
        "первую",
        "первые",
        "первый",
        "первым",
        "погиб",
        "погибла",
        "погибло",
        "появилась",
        "появились",
        "появился",
        "посещал",
        "произошла",
        "произошло",
        "происходит",
        "построен",
        "построена",
        "придуман",
        "придумана",
        "приземлился",
        "преземлился",
        "расстояние",
        "расположен",
        "расположена",
        "создан",
        "создана",
        "стал",
        "стала",
        "второе",
        "второго",
        "второй",
        "вторую",
        "вторые",
        "вторым",
        "умер",
        "умерла",
        "освободили",
        "полностью",
    }
)


def normalize_search_text(value: str) -> str:
    """Return NFC text without combining acute accents used in MIRACL RU."""
    decomposed = unicodedata.normalize("NFD", value)
    return unicodedata.normalize(
        "NFC", "".join(char for char in decomposed if char != "\N{COMBINING ACUTE ACCENT}")
    )


def build_lexical_query(value: str) -> str:
    """Build a deterministic BM25 query while preserving the semantic input.

    The returned string is safe from accidental YQL/full-text operators because
    only Unicode letter/number tokens survive.  Negation is intentionally not a
    stop word.  A single remaining content term is a valid selective keyword
    query (for example, "Что такое эритроциты?" -> "эритроциты").  The
    normalized source tokens are restored only when filtering removes every
    term.
    """
    tokens = _TOKEN.findall(normalize_search_text(value))
    filtered = [
        token
        for position, token in enumerate(tokens)
        if token.casefold() not in _RU_QUERY_STOP_WORDS
        # A capitalized token away from the sentence start may be a name, for
        # example "Ли Харви Освальд", rather than the particle "ли".
        or (
            position > 0
            and token[0].isupper()
            # Preserve the ambiguous particle only inside a likely personal
            # name (for example, "Ли Харви Освальд").  Treating every
            # capitalized function word as a name kept sentence-internal
            # "В"/"Во" and made it a disastrous required-term candidate.
            and token.casefold() == "ли"
        )
    ]
    selected = filtered if filtered else tokens
    return " ".join(selected)


def build_keyword_query(value: str, max_terms: int = 2) -> str:
    """Turn a lexical question into a compact, operator-free keyword query.

    The standalone full-text load models the common ``entity/topic`` search
    shape rather than a natural-language question. Generic intent verbs are
    removed and at most two informative source terms are kept. No answer,
    qrel or document text is used; the offline benchmark continues to use the
    complete ``lexical_query``.
    """
    if max_terms < 1:
        raise ValueError("max_terms must be positive")
    tokens = _TOKEN.findall(normalize_search_text(value))
    filtered = []
    seen = set()
    for position, token in enumerate(tokens):
        folded = token.casefold()
        if folded in _RU_KEYWORD_SCAFFOLDING or folded in seen:
            continue
        seen.add(folded)
        filtered.append((position, token))
    candidates = filtered if filtered else list(enumerate(tokens))
    if len(candidates) <= max_terms:
        return " ".join(token for _, token in candidates)

    # Prefer years/numbers, acronyms and proper names, then longer terms. Keep
    # their original order in the final query so examples remain readable.
    ranked = sorted(
        candidates,
        key=lambda item: (
            0 if item[1].isdecimal() else 1,
            0 if len(item[1]) > 1 and item[1].isupper() else 1,
            0 if item[1][:1].isupper() else 1,
            -len(item[1]),
            item[0],
        ),
    )[:max_terms]
    return " ".join(token for _, token in sorted(ranked))
