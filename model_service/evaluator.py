"""Evaluation metrics for regression models.

All metrics are computed in time-order without shuffling.
"""
import numpy as np
from typing import Dict, Optional


def mean_absolute_error(y_true: np.ndarray, y_pred: np.ndarray) -> float:
    mask = ~(np.isnan(y_true) | np.isnan(y_pred))
    if mask.sum() == 0:
        return np.nan
    return float(np.mean(np.abs(y_true[mask] - y_pred[mask])))


def root_mean_squared_error(y_true: np.ndarray, y_pred: np.ndarray) -> float:
    mask = ~(np.isnan(y_true) | np.isnan(y_pred))
    if mask.sum() == 0:
        return np.nan
    return float(np.sqrt(np.mean((y_true[mask] - y_pred[mask]) ** 2)))


def mean_absolute_percentage_error(y_true: np.ndarray, y_pred: np.ndarray) -> float:
    mask = ~(np.isnan(y_true) | np.isnan(y_pred))
    mask = mask & (np.abs(y_true) >= 1e-9)
    y_t = y_true[mask]
    y_p = y_pred[mask]
    if len(y_t) == 0:
        return np.nan
    return float(np.mean(np.abs((y_t - y_p) / np.maximum(np.abs(y_t), 1e-9))) * 100)


def r2_score(y_true: np.ndarray, y_pred: np.ndarray) -> float:
    mask = ~(np.isnan(y_true) | np.isnan(y_pred))
    if mask.sum() < 2:
        return np.nan
    y_t = y_true[mask]
    y_p = y_pred[mask]
    ss_res = np.sum((y_t - y_p) ** 2)
    ss_tot = np.sum((y_t - np.mean(y_t)) ** 2)
    if ss_tot < 1e-9:
        return np.nan
    return float(1 - ss_res / ss_tot)


def evaluate_all(y_true: np.ndarray, y_pred: np.ndarray) -> Dict[str, Optional[float]]:
    return {
        "mae": mean_absolute_error(y_true, y_pred),
        "rmse": root_mean_squared_error(y_true, y_pred),
        "mape": mean_absolute_percentage_error(y_true, y_pred),
        "r2": r2_score(y_true, y_pred),
    }
