from __future__ import annotations

import csv
import gzip
import hashlib
import heapq
import json
import unicodedata
from dataclasses import dataclass
from pathlib import Path
from typing import Iterator

@dataclass(frozen=True)
class Qrel:
    qid: str
    docid: str
    relevance: int


@dataclass(frozen=True)
class Document:
    docid: str
    title: str
    text: str


def document_text(title: str, body: str) -> str:
    """Build the one stored text representation for a MIRACL document.

    Castorini encoded the MIRACL ``title`` and ``text`` fields in this order,
    separated by two newlines. Removing combining acute accents preserves the
    source words while making the full-text representation deterministic. The
    vector branch uses the published embedding of the corresponding source
    fields rather than encoding this value again.
    """
    combined = f"{title.strip()}\n\n{body.strip()}".strip()
    decomposed = unicodedata.normalize("NFD", combined)
    return unicodedata.normalize("NFC", decomposed.replace("\u0301", ""))


def read_topics(path: Path) -> dict[str, str]:
    topics: dict[str, str] = {}
    with path.open("r", encoding="utf-8") as stream:
        for row in csv.reader(stream, delimiter="\t"):
            if len(row) >= 2:
                topics[row[0]] = row[1]
    return topics


def read_qrels(path: Path) -> list[Qrel]:
    result: list[Qrel] = []
    with path.open("r", encoding="utf-8") as stream:
        for row in csv.reader(stream, delimiter="\t"):
            if len(row) != 4:
                continue
            result.append(Qrel(row[0], row[2], int(row[3])))
    return result


def iter_corpus(paths: list[Path]) -> Iterator[Document]:
    for path in sorted(paths):
        with gzip.open(path, "rt", encoding="utf-8") as stream:
            for line in stream:
                row = json.loads(line)
                yield Document(str(row["docid"]), row.get("title") or "", row.get("text") or "")


def stable_score(docid: str, seed: int) -> int:
    payload = f"{seed}:{docid}".encode("utf-8")
    return int.from_bytes(hashlib.blake2b(payload, digest_size=8).digest(), "big")


def select_documents(
    documents: Iterator[Document], required_ids: set[str], size: int, seed: int
) -> tuple[dict[str, Document], int]:
    if size < len(required_ids):
        raise ValueError(f"size={size} is smaller than {len(required_ids)} judged documents")
    required: dict[str, Document] = {}
    background_count = size - len(required_ids)
    # Max-heap simulated by negative score: keep the lowest stable hashes.
    background: list[tuple[int, str, Document]] = []
    total = 0
    for document in documents:
        total += 1
        if document.docid in required_ids:
            required[document.docid] = document
            continue
        if not background_count:
            continue
        score = stable_score(document.docid, seed)
        item = (-score, document.docid, document)
        if len(background) < background_count:
            heapq.heappush(background, item)
        elif score < -background[0][0]:
            heapq.heapreplace(background, item)
    missing = required_ids - required.keys()
    if missing:
        sample = ", ".join(sorted(missing)[:5])
        raise RuntimeError(f"{len(missing)} judged documents are missing from corpus: {sample}")
    selected = dict(required)
    selected.update((item[2].docid, item[2]) for item in background)
    if len(selected) != size:
        raise RuntimeError(f"selected {len(selected)} documents instead of {size}")
    return selected, total
