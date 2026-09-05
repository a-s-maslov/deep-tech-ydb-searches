from __future__ import annotations

from pathlib import Path

import numpy as np
import pyarrow as pa
import pyarrow.parquet as pq


def encode_queries(
    source: Path,
    destination: Path,
    model_name: str,
    tokenizer_name: str,
    model_revision: str | None = None,
    tokenizer_revision: str | None = None,
    max_length: int = 32,
    batch_size: int = 64,
) -> None:
    try:
        import torch
        from transformers import AutoModel, AutoTokenizer
    except ImportError as error:
        raise RuntimeError("install model dependencies: pip install -e '.[model]'") from error
    table = pq.read_table(source)
    qids = table.column("qid").to_pylist()
    queries = table.column("query").to_pylist()
    tokenizer = AutoTokenizer.from_pretrained(
        tokenizer_name, revision=tokenizer_revision
    )
    model = AutoModel.from_pretrained(model_name, revision=model_revision)
    model.eval()
    device = torch.device("cuda" if torch.cuda.is_available() else "cpu")
    model.to(device)
    vectors: list[np.ndarray] = []
    with torch.inference_mode():
        for start in range(0, len(queries), batch_size):
            batch = tokenizer(
                queries[start : start + batch_size],
                max_length=max_length,
                padding=True,
                truncation=True,
                return_tensors="pt",
            )
            batch = {name: value.to(device) for name, value in batch.items()}
            output = model(**batch)
            # Tevatron's tied BERT encoder uses the CLS representation.
            encoded = output.last_hidden_state[:, 0].detach().cpu().float().numpy()
            vectors.extend(encoded)
    result = pa.table(
        {
            "qid": pa.array(qids, type=pa.string()),
            "query": pa.array(queries, type=pa.string()),
            "embedding": pa.array([x.tolist() for x in vectors], type=pa.list_(pa.float32(), 768)),
        }
    )
    pq.write_table(result, destination, compression="zstd")
