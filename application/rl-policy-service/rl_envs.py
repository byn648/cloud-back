from __future__ import annotations

import os
from typing import Any, Dict, List

import gymnasium as gym
import numpy as np
from gymnasium import spaces


MAX_CLUSTER_ACTIONS = int(os.getenv("MAX_CLUSTER_ACTIONS", "32"))
MAX_NODE_ACTIONS = int(os.getenv("MAX_NODE_ACTIONS", "128"))
GLOBAL_FEATURE_COUNT = 6

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


def observation_size(max_actions: int, feature_order: List[str]) -> int:
    return GLOBAL_FEATURE_COUNT + max_actions * len(feature_order)


def _value(obj: Any, key: str, default: Any = None) -> Any:
    if isinstance(obj, dict):
        return obj.get(key, default)
    return getattr(obj, key, default)


def build_action_mask(request: Any, max_actions: int) -> np.ndarray:
    candidates = list(_value(request, "candidates", []) or [])
    explicit_mask = list(_value(request, "actionMask", []) or [])
    mask = np.zeros(max_actions, dtype=bool)
    for index, candidate in enumerate(candidates[:max_actions]):
        allowed = bool(_value(candidate, "allowed", False))
        explicit = explicit_mask[index] if index < len(explicit_mask) else allowed
        mask[index] = bool(explicit and allowed)
    return mask


def build_observation(request: Any, feature_order: List[str], max_actions: int) -> np.ndarray:
    candidates = list(_value(request, "candidates", []) or [])
    service = _value(request, "service", {}) or {}
    matrix = np.zeros((max_actions, len(feature_order)), dtype=np.float32)
    for row, candidate in enumerate(candidates[:max_actions]):
        features = _value(candidate, "features", {}) or {}
        for col, feature_name in enumerate(feature_order):
            matrix[row, col] = float(_value(features, feature_name, 0.0) or 0.0)
    global_features = np.array(
        [
            float(_value(service, "replicas", 1) or 1),
            float(_value(service, "cpuRequestCores", 0.0) or 0.0),
            float(_value(service, "memoryRequestGiB", 0.0) or 0.0),
            float(_value(service, "gpuRequest", 0.0) or 0.0),
            float(_value(request, "pricePerKwh", 0.8) or 0.8),
            1.0 if _value(request, "priceSource", "default") == "default" else 0.0,
        ],
        dtype=np.float32,
    )
    return np.concatenate([global_features, matrix.reshape(-1)])


class RandomMaskedScheduleEnv(gym.Env):
    metadata = {"render_modes": []}

    def __init__(self, max_actions: int, feature_order: List[str]):
        super().__init__()
        self.max_actions = max_actions
        self.feature_order = feature_order
        self.action_space = spaces.Discrete(max_actions)
        self.observation_space = spaces.Box(
            low=-np.inf,
            high=np.inf,
            shape=(observation_size(max_actions, feature_order),),
            dtype=np.float32,
        )
        self._mask = np.zeros(max_actions, dtype=bool)
        self._obs = np.zeros(self.observation_space.shape, dtype=np.float32)

    def reset(self, *, seed: int | None = None, options: Dict[str, Any] | None = None):
        super().reset(seed=seed)
        allowed_count = int(self.np_random.integers(1, min(8, self.max_actions) + 1))
        allowed = self.np_random.choice(self.max_actions, size=allowed_count, replace=False)
        self._mask = np.zeros(self.max_actions, dtype=bool)
        self._mask[allowed] = True
        self._obs = self.np_random.normal(0, 0.05, size=self.observation_space.shape).astype(np.float32)
        return self._obs, {}

    def step(self, action: int):
        reward = 0.0 if self._mask[int(action)] else -1.0
        return self._obs, reward, True, False, {}

    def action_masks(self) -> np.ndarray:
        return self._mask
