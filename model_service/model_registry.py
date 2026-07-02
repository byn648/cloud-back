"""Model registry for loading and saving LightGBM models."""
import json
from pathlib import Path
from typing import Dict, List, Optional, Any

import joblib

from . import config


def get_model_path(target: str, horizon: int) -> Path:
    return config.CHECKPOINTS_DIR / f"lightgbm_{target}_h{horizon}.pkl"


def get_deep_model_path(model_name: str) -> Path:
    return config.CHECKPOINTS_DIR / f"deep_{model_name.lower()}.pt"


def get_deep_metadata_path(model_name: str) -> Path:
    return config.CHECKPOINTS_DIR / f"deep_{model_name.lower()}_metadata.json"


def save_model(model: Any, target: str, horizon: int) -> str:
    path = get_model_path(target, horizon)
    joblib.dump(model, str(path))
    return str(path)


def load_model(target: str, horizon: int):
    path = get_model_path(target, horizon)
    if not path.exists():
        raise FileNotFoundError(f"Model not found: {path}")
    return joblib.load(str(path))


def load_metadata() -> Dict[str, Any]:
    meta_path = config.CHECKPOINTS_DIR / "metadata.json"
    if not meta_path.exists():
        raise FileNotFoundError(f"Metadata not found: {meta_path}")
    with open(meta_path, "r") as f:
        return json.load(f)


def save_metadata(metadata: Dict[str, Any]):
    meta_path = config.CHECKPOINTS_DIR / "metadata.json"
    with open(meta_path, "w") as f:
        json.dump(metadata, f, indent=2, ensure_ascii=False)


def load_deep_metadata(model_name: str) -> Dict[str, Any]:
    meta_path = get_deep_metadata_path(model_name)
    if not meta_path.exists():
        raise FileNotFoundError(f"Deep metadata not found: {meta_path}")
    with open(meta_path, "r") as f:
        return json.load(f)


def save_deep_metadata(model_name: str, metadata: Dict[str, Any]):
    meta_path = get_deep_metadata_path(model_name)
    with open(meta_path, "w") as f:
        json.dump(metadata, f, indent=2, ensure_ascii=False)


def list_available_models(horizons: List[int], targets: List[str]) -> Dict[str, bool]:
    available = {}
    for target in targets:
        for h in horizons:
            key = f"{target}_h{h}"
            available[key] = get_model_path(target, h).exists()
    return available


def get_latest_version() -> str:
    meta_path = config.CHECKPOINTS_DIR / "metadata.json"
    if meta_path.exists():
        with open(meta_path) as f:
            meta = json.load(f)
        return meta.get("model_version", config.MODEL_VERSION)
    return config.MODEL_VERSION
