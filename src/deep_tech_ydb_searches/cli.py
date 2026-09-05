from __future__ import annotations

import argparse
import json
import shutil
import tarfile
from pathlib import Path

from .artifacts import write_smoke
from .config import load_dataset_profile, load_sources
from .download import download_multipart, download_resumable, write_download_manifest
from .faiss_tools import extract_vectors, find_positions, locate_index
from .miracl import document_text, iter_corpus, read_qrels, read_topics, select_documents
from .paths import data_root
from .query_encoder import encode_queries
from .quality import build_exact_artifact
from .validate import validate_smoke


def _output_root(args: argparse.Namespace) -> Path:
    configured = getattr(args, "output_dir", None)
    return Path(configured) if configured else data_root() / "output" / f"smoke-{args.size}"


def cmd_make_fixture(args: argparse.Namespace) -> None:
    """Create a tiny structural fixture; never used as workshop corpus."""
    import numpy as np
    import pyarrow as pa
    import pyarrow.parquet as pq
    from .download import file_hash

    root = _output_root(args)
    docs = root / "documents"
    docs.mkdir(parents=True, exist_ok=True)
    phrases = [
        ("Ошибка подключения к сервису", "Сервис временно недоступен: таймаут подключения 503"),
        ("Распределённая база данных", "Партиционирование и репликация обеспечивают масштабирование"),
        ("Векторный поиск", "Поиск документов, близких по смыслу, по готовым эмбеддингам"),
        ("Полнотекстовый поиск", "Инвертированный индекс находит точные слова и фразы"),
    ]
    rows = []
    for index in range(args.size):
        title, body = phrases[index % len(phrases)]
        rng = np.random.default_rng(index)
        vector = rng.normal(size=768).astype(np.float32)
        rows.append((index, f"fixture-{index}", title, body, vector.tolist()))
    schema_type = pa.list_(pa.float32(), 768)
    table = pa.table({
        "id": pa.array([x[0] for x in rows], type=pa.uint64()),
        "docid": pa.array([x[1] for x in rows]),
        "title": pa.array([x[2] for x in rows]),
        "text": pa.array([document_text(x[2], x[3]) for x in rows]),
        "embedding": pa.array([x[4] for x in rows], type=schema_type),
    })
    doc_path = docs / "part-00000.parquet"
    pq.write_table(table, doc_path, compression="zstd")
    query_vector = rows[0][4]
    pq.write_table(pa.table({
        "qid": pa.array(["fixture-q1"]),
        "query": pa.array(["ошибка подключения"]),
    }), root / "queries.parquet")
    query_path = root / "queries-embedded.parquet"
    pq.write_table(pa.table({
        "qid": pa.array(["fixture-q1"]),
        "query": pa.array(["ошибка подключения"]),
        "embedding": pa.array([query_vector], type=schema_type),
    }), query_path)
    qrels_path = root / "qrels.parquet"
    pq.write_table(pa.table({
        "qid": pa.array(["fixture-q1"]),
        "docid": pa.array(["fixture-0"]),
        "relevance": pa.array([1], type=pa.int32()),
    }), qrels_path)
    tracked = [doc_path, root / "queries.parquet", qrels_path]
    manifest = {
        "format_version": 2,
        "profile": f"structural-fixture-{args.size}",
        "documents": args.size,
        "queries": 1,
        "qrels": 1,
        "vector": {"dimension": 768, "dtype": "float32", "metric": "inner_product"},
        "document": {
            "fields": ["id", "docid", "title", "text", "embedding"],
            "text": "normalized(title + two newlines + MIRACL passage)",
        },
        "files": [{
            "path": str(path.relative_to(root)), "size": path.stat().st_size,
            "sha256": file_hash(path),
        } for path in tracked],
    }
    (root / "manifest.json").write_text(json.dumps(manifest, indent=2), encoding="utf-8")
    print(root)


def _safe_unpack(archive: Path, destination: Path) -> None:
    destination.mkdir(parents=True, exist_ok=True)
    root = destination.resolve()
    with tarfile.open(archive, "r:gz") as stream:
        for member in stream:
            target = (destination / member.name).resolve()
            if root not in target.parents and target != root:
                raise RuntimeError(f"unsafe archive member: {member.name}")
            stream.extract(member, destination, filter="data")


def cmd_download_index(args: argparse.Namespace) -> None:
    source = load_sources()["castorini_index"]
    path = data_root() / "raw" / "castorini" / source["filename"]
    download_multipart(
        source["url"], path, expected_size=source["size"], expected_md5=source["md5"],
        workers=args.workers,
    )
    print(path)


def cmd_unpack_index(_: argparse.Namespace) -> None:
    source = load_sources()["castorini_index"]
    archive = data_root() / "raw" / "castorini" / source["filename"]
    destination = data_root() / "work" / "castorini"
    marker = destination / ".unpacked-source.json"
    expected = {
        "filename": source["filename"],
        "size": source["size"],
        "md5": source["md5"],
    }
    if marker.exists() and json.loads(marker.read_text(encoding="utf-8")) == expected:
        print(locate_index(destination))
        return
    _safe_unpack(archive, destination)
    locate_index(destination)
    marker.write_text(json.dumps(expected, indent=2), encoding="utf-8")
    print(locate_index(destination))


def cmd_download_miracl(_: argparse.Namespace) -> None:
    source = load_sources()["miracl"]
    root = data_root() / "raw" / "miracl"
    files = [
        download_resumable(source["topics"], root / "topics.dev.tsv"),
        download_resumable(source["qrels"], root / "qrels.dev.tsv"),
    ]
    corpus = root / "corpus"
    for number in range(source["corpus_files"]):
        url = source["corpus_url_template"].format(number=number)
        files.append(download_resumable(url, corpus / f"docs-{number}.jsonl.gz"))
    write_download_manifest(files, root / "download-manifest.json")
    print(f"downloaded {len(files)} files")


def cmd_build_smoke(args: argparse.Namespace) -> None:
    raw = data_root() / "raw" / "miracl"
    topics = read_topics(raw / "topics.dev.tsv")
    qrels = read_qrels(raw / "qrels.dev.tsv")
    required = {item.docid for item in qrels}
    documents, corpus_rows = select_documents(
        iter_corpus(list((raw / "corpus").glob("*.jsonl.gz"))), required, args.size, args.seed
    )
    index_path, docid_path = locate_index(data_root() / "work" / "castorini")
    positions, index_rows = find_positions(docid_path, set(documents))
    if index_rows != corpus_rows:
        raise RuntimeError(
            f"corpus/index row mismatch: corpus={corpus_rows}, index={index_rows}"
        )
    vectors = extract_vectors(index_path, positions, index_rows)
    destination = _output_root(args)
    if destination.exists():
        shutil.rmtree(destination)
    write_smoke(
        destination, documents, positions, vectors, topics, qrels,
        seed=args.seed, corpus_rows=corpus_rows,
        profile=getattr(args, "dataset_profile", None),
    )
    print(json.dumps(validate_smoke(destination, require_query_vectors=False), indent=2))


def cmd_encode_queries(args: argparse.Namespace) -> None:
    from .download import file_hash

    cfg = load_sources()["query_encoder"]
    root = _output_root(args)
    output = root / "queries-embedded.parquet"
    encode_queries(
        root / "queries.parquet",
        output,
        cfg["model"],
        cfg["tokenizer"],
        cfg["model_revision"],
        cfg["tokenizer_revision"],
        cfg["max_length"],
        args.batch_size,
    )
    manifest_path = root / "manifest.json"
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    record = {
        "path": output.name,
        "size": output.stat().st_size,
        "sha256": file_hash(output),
    }
    manifest["files"] = [item for item in manifest["files"] if item["path"] != output.name]
    manifest["files"].append(record)
    manifest["query_encoder"] = {
        "model": cfg["model"],
        "model_revision": cfg["model_revision"],
        "tokenizer": cfg["tokenizer"],
        "tokenizer_revision": cfg["tokenizer_revision"],
        "max_length": cfg["max_length"],
    }
    manifest_path.write_text(json.dumps(manifest, ensure_ascii=False, indent=2), encoding="utf-8")
    print(output)


def cmd_validate(args: argparse.Namespace) -> None:
    root = _output_root(args)
    print(json.dumps(validate_smoke(root), indent=2))


def cmd_export_workload(args: argparse.Namespace) -> None:
    from .workload_artifact import export_workload_artifact, write_runtime_checksums

    if getattr(args, "profile", ""):
        profile = load_dataset_profile(args.profile)
        args.size = int(profile["size"])
        args.output_dir = profile["output_dir"]
    root = _output_root(args)
    query_output = Path(args.output) if args.output else root / "workload-queries.json.gz"
    document_output = root / "workload-documents.jsonl.gz"
    result = export_workload_artifact(root, query_output, document_output)
    result["checksums"] = str(write_runtime_checksums(query_output, document_output))
    print(json.dumps(result, ensure_ascii=False, indent=2))


def cmd_export_queries(args: argparse.Namespace) -> None:
    """Rebuild query-side representations without rewriting all documents."""
    from .workload_artifact import export_query_artifact, write_runtime_checksums

    if getattr(args, "profile", ""):
        profile = load_dataset_profile(args.profile)
        args.size = int(profile["size"])
        args.output_dir = profile["output_dir"]
    root = _output_root(args)
    query_output = Path(args.output) if args.output else root / "workload-queries.json.gz"
    document_output = root / "workload-documents.jsonl.gz"
    if not document_output.exists():
        raise FileNotFoundError(f"document runtime artifact is missing: {document_output}")
    result = export_query_artifact(root, query_output)
    result["checksums"] = str(write_runtime_checksums(query_output, document_output))
    print(json.dumps(result, ensure_ascii=False, indent=2))


def cmd_build_exact(args: argparse.Namespace) -> None:
    if args.profile:
        profile = load_dataset_profile(args.profile)
        args.size = int(profile["size"])
        args.output_dir = profile["output_dir"]
    else:
        args.output_dir = None
    root = _output_root(args)
    output = Path(args.output) if args.output else root / "exact-top30.json.gz"
    result = build_exact_artifact(root, output, top_k=args.top_k)
    print(json.dumps(result, ensure_ascii=False, indent=2))


def cmd_prepare_runtime(args: argparse.Namespace) -> None:
    """Run the single official-source-to-runtime pipeline."""
    if args.profile:
        profile = load_dataset_profile(args.profile)
        args.size = int(profile["size"])
        args.seed = int(profile["seed"])
        args.output_dir = profile["output_dir"]
        args.dataset_profile = profile["name"]
    else:
        args.output_dir = None
        args.dataset_profile = None
    print("[1/7] Download and verify the official Castorini index", flush=True)
    cmd_download_index(argparse.Namespace(workers=args.download_workers))
    print("[2/7] Unpack the verified source index (cached after success)", flush=True)
    cmd_unpack_index(argparse.Namespace())
    print("[3/7] Download MIRACL RU corpus, topics and qrels", flush=True)
    cmd_download_miracl(argparse.Namespace())
    print(f"[4/7] Build deterministic {args.dataset_profile or f'smoke-{args.size}'}", flush=True)
    common = {"size": args.size, "output_dir": args.output_dir}
    cmd_build_smoke(argparse.Namespace(
        **common, seed=args.seed, dataset_profile=args.dataset_profile
    ))
    print("[5/7] Encode official dev queries with the matching public checkpoint", flush=True)
    cmd_encode_queries(
        argparse.Namespace(**common, batch_size=args.query_batch_size)
    )
    print("[6/7] Validate ids, dimensions, qrels and intermediate checksums", flush=True)
    cmd_validate(argparse.Namespace(**common))
    print("[7/7] Export Go runtime files and SHA-256 checksums", flush=True)
    cmd_export_workload(argparse.Namespace(**common, output=""))


def parser() -> argparse.ArgumentParser:
    result = argparse.ArgumentParser(description="Prepare MIRACL RU data for YDB")
    commands = result.add_subparsers(dest="command", required=True)
    for name, handler in (
        ("unpack-index", cmd_unpack_index),
        ("download-miracl", cmd_download_miracl),
    ):
        command = commands.add_parser(name)
        command.set_defaults(handler=handler)
    index_download = commands.add_parser("download-index")
    index_download.add_argument("--workers", type=int, default=8)
    index_download.set_defaults(handler=cmd_download_index)
    fixture = commands.add_parser("make-fixture")
    fixture.add_argument("--size", type=int, default=100)
    fixture.set_defaults(handler=cmd_make_fixture)
    build = commands.add_parser("build-smoke")
    build.add_argument("--size", type=int, default=50000)
    build.add_argument("--seed", type=int, default=20260825)
    build.set_defaults(handler=cmd_build_smoke)
    encode = commands.add_parser("encode-queries")
    encode.add_argument("--size", type=int, default=50000)
    encode.add_argument("--batch-size", type=int, default=64)
    encode.set_defaults(handler=cmd_encode_queries)
    validate = commands.add_parser("validate-smoke")
    validate.add_argument("--size", type=int, default=50000)
    validate.set_defaults(handler=cmd_validate)
    workload = commands.add_parser("export-workload")
    workload.add_argument("--size", type=int, default=50000)
    workload.add_argument("--profile", default="", help="named profile from config/datasets.json")
    workload.add_argument("--output", default="")
    workload.set_defaults(handler=cmd_export_workload)
    queries = commands.add_parser(
        "export-queries",
        help="rebuild lexical/query runtime data without rewriting documents",
    )
    queries.add_argument("--size", type=int, default=50000)
    queries.add_argument("--profile", default="", help="named profile from config/datasets.json")
    queries.add_argument("--output", default="")
    queries.set_defaults(handler=cmd_export_queries)
    exact = commands.add_parser(
        "build-exact",
        help="build an exact inner-product top-k reference for a prepared profile",
    )
    exact.add_argument("--size", type=int, default=50000)
    exact.add_argument("--profile", default="", help="named profile from config/datasets.json")
    exact.add_argument("--top-k", type=int, default=30)
    exact.add_argument("--output", default="")
    exact.set_defaults(handler=cmd_build_exact)
    prepare = commands.add_parser(
        "prepare-runtime",
        help="download official sources and produce verified Go runtime files",
    )
    prepare.add_argument("--size", type=int, default=50000)
    prepare.add_argument("--seed", type=int, default=20260825)
    prepare.add_argument("--profile", default="", help="named profile from config/datasets.json")
    prepare.add_argument("--download-workers", type=int, default=8)
    prepare.add_argument("--query-batch-size", type=int, default=64)
    prepare.set_defaults(handler=cmd_prepare_runtime)
    return result


def main() -> None:
    args = parser().parse_args()
    args.handler(args)


if __name__ == "__main__":
    main()
