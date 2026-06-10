import os
from pathlib import Path
from typing import Dict, List, Optional

import numpy as np
from fastapi import FastAPI, HTTPException
from pydantic import BaseModel, Field

try:
    from sb3_contrib import MaskablePPO
except Exception:  # pragma: no cover - lets the service start before deps/models are ready
    MaskablePPO = None


MAX_CLUSTER_ACTIONS = int(os.getenv("MAX_CLUSTER_ACTIONS", "32"))
MAX_NODE_ACTIONS = int(os.getenv("MAX_NODE_ACTIONS", "128"))
ALLOW_MODEL_FALLBACK = os.getenv("RL_POLICY_ALLOW_FALLBACK", "false").lower() in {"1", "true", "yes"}
MODEL_DIR = Path(os.getenv("RL_POLICY_MODEL_DIR", "models"))

CLUSTER_FEATURE_ORDER = [
    "authorized",
    "status_normal",
    "realtime_resource_ready",
    "cpu_free_ratio",
    "memory_free_ratio",
    "gpu_free_ratio",
    "has_current_power",
    "current_power_norm",
    "price_per_kwh",
    "price_is_default",
    "storage_soc",
    "has_storage_soc",
]

NODE_FEATURE_ORDER = [
    "cluster_present",
    "node_ready",
    "unschedulable",
    "cpu_free_ratio",
    "memory_free_ratio",
    "gpu_free_ratio",
    "pods_free_norm",
    "assigned_replicas",
    "has_current_power",
    "current_power_norm",
    "price_per_kwh",
    "price_is_default",
]


class ServiceDemand(BaseModel):
    replicas: int = 1
    cpuRequestCores: float = 0
    memoryRequestGiB: float = 0
    gpuRequest: float = 0


class ActionCandidate(BaseModel):
    id: str = ""
    name: str = ""
    allowed: bool = False
    features: Dict[str, float] = Field(default_factory=dict)


class PredictRequest(BaseModel):
    model: str
    level: str
    service: ServiceDemand
    candidates: List[ActionCandidate]
    actionMask: List[bool]
    replica: Optional[int] = None
    pricePerKwh: float = 0.8
    priceSource: str = "default"


class PredictResponse(BaseModel):
    actionIndex: int
    modelVersion: str
    reason: str = ""


class PolicyBundle:
    def __init__(self, model_name: str, model_path: Path, max_actions: int, feature_order: List[str]):
        self.model_name = model_name
        self.model_path = model_path
        self.max_actions = max_actions
        self.feature_order = feature_order
        self.model = None
        self.load_error = ""
        self.reload()

    def reload(self) -> None:
        self.model = None
        self.load_error = ""
        if MaskablePPO is None:
            self.load_error = "sb3-contrib MaskablePPO is not installed"
            return
        if not self.model_path.exists():
            self.load_error = f"model file not found: {self.model_path}"
            return
        try:
            self.model = MaskablePPO.load(str(self.model_path))
        except Exception as exc:  # pragma: no cover
            self.load_error = str(exc)

    def predict(self, request: PredictRequest) -> PredictResponse:
        if len(request.candidates) > self.max_actions:
            raise HTTPException(
                status_code=400,
                detail=f"{request.level} candidates exceed max_actions={self.max_actions}",
            )
        allowed = build_action_mask(request.actionMask, request.candidates, self.max_actions)
        if not allowed[: len(request.candidates)].any():
            raise HTTPException(status_code=400, detail="no allowed action")

        if self.model is None:
            if ALLOW_MODEL_FALLBACK:
                index = int(np.flatnonzero(allowed[: len(request.candidates)])[0])
                return PredictResponse(
                    actionIndex=index,
                    modelVersion=self.model_name,
                    reason=f"fallback:first-allowed; {self.load_error}",
                )
            raise HTTPException(
                status_code=503,
                detail=f"model is not ready: {self.model_path}; {self.load_error}",
            )

        obs = build_observation(request, self.feature_order, self.max_actions)
        action, _ = self.model.predict(obs, action_masks=allowed, deterministic=True)
        index = int(action)
        if index >= len(request.candidates) or not allowed[index]:
            if ALLOW_MODEL_FALLBACK:
                index = int(np.flatnonzero(allowed[: len(request.candidates)])[0])
                return PredictResponse(
                    actionIndex=index,
                    modelVersion=self.model_name,
                    reason="fallback:model-returned-invalid-action",
                )
            raise HTTPException(status_code=500, detail="model returned masked or out-of-range action")
        return PredictResponse(actionIndex=index, modelVersion=self.model_name)


def build_action_mask(action_mask: List[bool], candidates: List[ActionCandidate], max_actions: int) -> np.ndarray:
    mask = np.zeros(max_actions, dtype=bool)
    for index, candidate in enumerate(candidates[:max_actions]):
        explicit = action_mask[index] if index < len(action_mask) else candidate.allowed
        mask[index] = bool(explicit and candidate.allowed)
    return mask


def build_observation(request: PredictRequest, feature_order: List[str], max_actions: int) -> np.ndarray:
    matrix = np.zeros((max_actions, len(feature_order)), dtype=np.float32)
    for row, candidate in enumerate(request.candidates[:max_actions]):
        for col, feature_name in enumerate(feature_order):
            matrix[row, col] = float(candidate.features.get(feature_name, 0.0))
    global_features = np.array(
        [
            float(request.service.replicas),
            float(request.service.cpuRequestCores),
            float(request.service.memoryRequestGiB),
            float(request.service.gpuRequest),
            float(request.pricePerKwh),
            1.0 if request.priceSource == "default" else 0.0,
        ],
        dtype=np.float32,
    )
    return np.concatenate([global_features, matrix.reshape(-1)])


cluster_policy = PolicyBundle(
    model_name=os.getenv("CLUSTER_POLICY_MODEL", "maskable-ppo-cluster-v1"),
    model_path=Path(os.getenv("CLUSTER_POLICY_PATH", MODEL_DIR / "cluster_policy.zip")),
    max_actions=MAX_CLUSTER_ACTIONS,
    feature_order=CLUSTER_FEATURE_ORDER,
)
node_policy = PolicyBundle(
    model_name=os.getenv("NODE_POLICY_MODEL", "maskable-ppo-node-v1"),
    model_path=Path(os.getenv("NODE_POLICY_PATH", MODEL_DIR / "node_policy.zip")),
    max_actions=MAX_NODE_ACTIONS,
    feature_order=NODE_FEATURE_ORDER,
)


app = FastAPI(title="Kube Nova RL Policy Service")


@app.get("/healthz")
def healthz():
    return {
        "status": "ok" if cluster_policy.model is not None and node_policy.model is not None else "degraded",
        "clusterModelLoaded": cluster_policy.model is not None,
        "nodeModelLoaded": node_policy.model is not None,
        "clusterLoadError": cluster_policy.load_error,
        "nodeLoadError": node_policy.load_error,
        "allowFallback": ALLOW_MODEL_FALLBACK,
    }


@app.post("/v1/predict/cluster", response_model=PredictResponse)
def predict_cluster(request: PredictRequest):
    if request.level != "cluster":
        raise HTTPException(status_code=400, detail="level must be cluster")
    return cluster_policy.predict(request)


@app.post("/v1/predict/node", response_model=PredictResponse)
def predict_node(request: PredictRequest):
    if request.level != "node":
        raise HTTPException(status_code=400, detail="level must be node")
    return node_policy.predict(request)


@app.post("/v1/models/reload")
def reload_models():
    cluster_policy.reload()
    node_policy.reload()
    return healthz()
