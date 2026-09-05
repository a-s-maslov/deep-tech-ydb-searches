from __future__ import annotations

import hashlib
import json
import shutil
import time
from concurrent.futures import ThreadPoolExecutor, as_completed
from pathlib import Path
from typing import Iterable

import requests
from tqdm import tqdm


def file_hash(path: Path, algorithm: str = "sha256", chunk_size: int = 8 << 20) -> str:
    digest = hashlib.new(algorithm)
    with path.open("rb") as stream:
        while chunk := stream.read(chunk_size):
            digest.update(chunk)
    return digest.hexdigest()


def download_resumable(
    url: str,
    destination: Path,
    *,
    expected_size: int | None = None,
    expected_md5: str | None = None,
    chunk_size: int = 8 << 20,
) -> Path:
    destination.parent.mkdir(parents=True, exist_ok=True)
    existing = destination.stat().st_size if destination.exists() else 0
    if expected_size is not None and existing == expected_size:
        if expected_md5 is None or file_hash(destination, "md5") == expected_md5:
            return destination
    if expected_size is not None and existing > expected_size:
        raise ValueError(f"{destination} is larger than the expected source")

    headers = {"Range": f"bytes={existing}-"} if existing else {}
    with requests.get(url, headers=headers, stream=True, timeout=(30, 120)) as response:
        if response.status_code == 416 and existing:
            content_range = response.headers.get("content-range", "")
            remote_size = int(content_range.rsplit("/", 1)[1]) if "/" in content_range else None
            # Some object-storage CDNs omit Content-Range on a 416 response.
            # Probe the same immutable object without Range and compare its
            # advertised length before accepting the local file as complete.
            if remote_size is None:
                with requests.get(url, stream=True, timeout=(30, 120)) as probe:
                    probe.raise_for_status()
                    length = probe.headers.get("content-length")
                    remote_size = int(length) if length is not None else None
            if remote_size == existing:
                return destination
            if remote_size is not None and existing > remote_size:
                raise ValueError(f"{destination} is larger than the remote source")
        response.raise_for_status()
        if existing and response.status_code != 206:
            raise RuntimeError("server ignored Range; refusing to overwrite partial download")
        total = expected_size or (existing + int(response.headers.get("content-length", "0")))
        mode = "ab" if existing else "wb"
        with destination.open(mode) as target, tqdm(
            total=total, initial=existing, unit="B", unit_scale=True, desc=destination.name
        ) as progress:
            for chunk in response.iter_content(chunk_size=chunk_size):
                if chunk:
                    target.write(chunk)
                    progress.update(len(chunk))

    if expected_size is not None and destination.stat().st_size != expected_size:
        raise RuntimeError("downloaded size does not match manifest")
    if expected_md5 is not None and file_hash(destination, "md5") != expected_md5:
        raise RuntimeError("downloaded MD5 does not match manifest")
    return destination


def download_multipart(
    url: str,
    destination: Path,
    *,
    expected_size: int,
    expected_md5: str | None = None,
    workers: int = 8,
    segment_size: int = 2 << 30,
    range_size: int = 256 << 20,
    chunk_size: int = 8 << 20,
) -> Path:
    """Download a large range-capable object concurrently and resumably."""
    destination.parent.mkdir(parents=True, exist_ok=True)
    if destination.exists() and destination.stat().st_size == expected_size:
        if expected_md5 is None or file_hash(destination, "md5") == expected_md5:
            return destination

    parts_root = destination.with_name(destination.name + ".parts")
    parts_root.mkdir(parents=True, exist_ok=True)
    first_part = parts_root / "part-00000"
    # Preserve a partial single-stream download instead of starting over.
    if destination.exists() and not first_part.exists():
        if destination.stat().st_size > segment_size:
            raise RuntimeError("partial source is larger than the first multipart segment")
        destination.replace(first_part)

    ranges = []
    for number, start in enumerate(range(0, expected_size, segment_size)):
        end = min(start + segment_size, expected_size) - 1
        ranges.append((number, start, end, parts_root / f"part-{number:05d}"))

    pending: dict[int, list[tuple[int, int, Path]]] = {}
    tasks: list[tuple[int, int, Path]] = []
    for number, start, end, path in ranges:
        required = end - start + 1
        existing = path.stat().st_size if path.exists() else 0
        if existing > required:
            raise RuntimeError(f"multipart segment {number} is too large")
        pending[number] = []
        chunks_root = parts_root / f"part-{number:05d}.chunks"
        for chunk_start in range(start + existing, end + 1, range_size):
            chunk_end = min(chunk_start + range_size, end + 1) - 1
            chunk_path = chunks_root / f"{chunk_start - start:012d}"
            item = (chunk_start, chunk_end, chunk_path)
            pending[number].append(item)
            tasks.append(item)

    def fetch(item: tuple[int, int, Path]) -> tuple[Path, int]:
        start, end, path = item
        path.parent.mkdir(parents=True, exist_ok=True)
        required = end - start + 1
        for attempt in range(1, 6):
            existing = path.stat().st_size if path.exists() else 0
            if existing > required:
                raise RuntimeError(f"multipart range {path} is too large")
            if existing == required:
                return path, required
            headers = {"Range": f"bytes={start + existing}-{end}"}
            try:
                with requests.get(url, headers=headers, stream=True, timeout=(30, 30)) as response:
                    response.raise_for_status()
                    if response.status_code != 206:
                        raise RuntimeError("server ignored Range; multipart download is unsafe")
                    with path.open("ab") as target:
                        for chunk in response.iter_content(chunk_size=chunk_size):
                            if chunk:
                                target.write(chunk)
            except requests.RequestException:
                if attempt == 5:
                    raise
                time.sleep(2 ** (attempt - 1))
                continue
        if path.stat().st_size != required:
            raise RuntimeError(f"multipart range {path} has an unexpected size")
        return path, required

    with ThreadPoolExecutor(max_workers=workers) as executor:
        futures = [executor.submit(fetch, item) for item in tasks]
        for future in as_completed(futures):
            path, size = future.result()
            print(f"downloaded range {path.name} ({size:,} bytes)", flush=True)

    for number, start, end, path in ranges:
        required = end - start + 1
        existing = path.stat().st_size if path.exists() else 0
        if existing < required:
            with path.open("ab") as target:
                for _, _, chunk_path in pending[number]:
                    with chunk_path.open("rb") as source:
                        shutil.copyfileobj(source, target, length=chunk_size)
        if path.stat().st_size != required:
            raise RuntimeError(f"multipart segment {number} has an unexpected size")
        chunks_root = parts_root / f"part-{number:05d}.chunks"
        if chunks_root.exists():
            shutil.rmtree(chunks_root)
        print(f"completed segment {number + 1}/{len(ranges)} ({required:,} bytes)", flush=True)

    assembling = destination.with_name(destination.name + ".assembling")
    with assembling.open("wb") as target:
        for _, _, _, path in ranges:
            with path.open("rb") as source:
                shutil.copyfileobj(source, target, length=chunk_size)
    if assembling.stat().st_size != expected_size:
        raise RuntimeError("assembled download does not match manifest size")
    if expected_md5 is not None and file_hash(assembling, "md5") != expected_md5:
        raise RuntimeError("assembled download MD5 does not match manifest")
    assembling.replace(destination)
    shutil.rmtree(parts_root)
    return destination


def write_download_manifest(paths: Iterable[Path], destination: Path) -> None:
    records = [
        {"path": str(path), "size": path.stat().st_size, "sha256": file_hash(path)}
        for path in paths
    ]
    destination.parent.mkdir(parents=True, exist_ok=True)
    destination.write_text(json.dumps(records, ensure_ascii=False, indent=2), encoding="utf-8")
