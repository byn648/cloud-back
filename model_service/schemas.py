"""Data models and schemas for model service."""
from typing import Optional, List, Dict, Any
from datetime import datetime
from enum import Enum
from pydantic import BaseModel, Field


class RiskLevel(str, Enum):
    LOW = "LOW"
    MEDIUM = "MEDIUM"
    HIGH = "HIGH"


class RawMetricRecord(BaseModel):
    node_uuid: str
    cluster_uuid: str
    metric_time: datetime
    cpu_util: Optional[float] = None
    gpu_util: Optional[float] = None
    gpu_mem_util: Optional[float] = None
    mem_util: Optional[float] = None
    node_power: Optional[float] = None
    disk_io: Optional[float] = None
    net_io: Optional[float] = None
    rated_power: Optional[float] = None
    gpu_count: Optional[int] = None
    cpu_model: Optional[str] = None
    gpu_model: Optional[str] = None


class FeatureSample(BaseModel):
    window_start_time: datetime
    sample_time: datetime
    target_time: datetime
    X: Dict[str, float]
    y: Dict[str, float]


class TrainConfig(BaseModel):
    cluster_uuid: str
    start_time: str
    end_time: str
    horizons: List[int] = Field(default_factory=lambda: [15, 30, 60])
    history_window: int = 60
    resample_freq: str = "1min"
    max_missing_ratio: float = 0.3


class PredictConfig(BaseModel):
    cluster_uuid: str
    node_uuids: List[str]
    horizons: List[int] = Field(default_factory=lambda: [15, 30, 60])


class PredictionItem(BaseModel):
    horizon: int
    forecast_time: str
    metrics: Dict[str, float]
    quantiles: Optional[Dict[str, Dict[str, float]]] = None
    confidence_interval: Optional[Dict[str, Dict[str, float]]] = None
    overload_risk: Optional[Dict[str, float]] = None
    slo_violation_risk: Optional[float] = None
    risk_level: str


class NodePredictionResult(BaseModel):
    node_uuid: str
    predictions: Optional[List[PredictionItem]] = None
    error: Optional[str] = None


class PredictResponse(BaseModel):
    cluster_uuid: str
    horizons: List[int]
    model_version: str
    results: List[NodePredictionResult]


class MetricPoint(BaseModel):
    time: str
    cpu_util: Optional[float] = None
    gpu_util: Optional[float] = None
    gpu_mem_util: Optional[float] = None
    node_power: Optional[float] = None


class HistoryResponse(BaseModel):
    node_uuid: str
    cluster_uuid: str
    start_time: str
    end_time: str
    metrics: List[MetricPoint]


class ModelMetrics(BaseModel):
    mae: Optional[float] = None
    rmse: Optional[float] = None
    mape: Optional[float] = None
    r2: Optional[float] = None


class MetadataResponse(BaseModel):
    model_version: str
    created_at: str
    horizons: List[int]
    target_columns: List[str]
    feature_columns: List[str]
    metrics: Dict[str, Dict[str, float]]
    warnings: List[str] = Field(default_factory=list)
