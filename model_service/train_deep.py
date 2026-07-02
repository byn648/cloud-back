"""Train PatchTST, iTransformer, or Hetero-STGNN multivariate load forecasters."""
import argparse
import json
import sys
from datetime import datetime
from pathlib import Path
from typing import Dict, List

import numpy as np

if __package__ in (None, ""):
    sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
    from model_service import config
    from model_service import data_loader
    from model_service import deep_dataset
    from model_service import evaluator
    from model_service import model_registry
else:
    from . import config
    from . import data_loader
    from . import deep_dataset
    from . import evaluator
    from . import model_registry


def _import_torch():
    try:
        import torch
        from torch import nn
        from torch.utils.data import DataLoader, TensorDataset

        if __package__ in (None, ""):
            from model_service.deep_models import HeteroSTGNNLoadNet, ITransformer, PatchTST
        else:
            from .deep_models import HeteroSTGNNLoadNet, ITransformer, PatchTST
    except ModuleNotFoundError as exc:
        raise RuntimeError(
            "PyTorch is required for heterogeneous deep models. Install it with: "
            "python3 -m pip install torch"
        ) from exc
    return torch, nn, DataLoader, TensorDataset, PatchTST, ITransformer, HeteroSTGNNLoadNet


def _parse_list(value: str, cast=str) -> List:
    return [cast(item.strip()) for item in value.split(",") if item.strip()]


def _normalize_model_name(model_name: str) -> str:
    name = model_name.lower().replace("-", "_")
    if name in {"hetero_stgnn", "heterostgnn", "hetero_stgnn_loadnet"}:
        return "heterostgnn"
    return name


def _make_model(args, n_features: int, target_indices: List[int], pred_len: int, model_classes, context_encoder=None):
    PatchTST, ITransformer, HeteroSTGNNLoadNet = model_classes
    cat_cardinalities = context_encoder.cat_cardinalities if context_encoder else []
    n_numeric_context = len(context_encoder.numeric_columns) if context_encoder else 0
    common = {
        "n_features": n_features,
        "target_indices": target_indices,
        "pred_len": pred_len,
        "d_model": args.d_model,
        "n_heads": args.n_heads,
        "e_layers": args.e_layers,
        "d_ff": args.d_ff,
        "dropout": args.dropout,
        "cat_cardinalities": cat_cardinalities,
        "n_numeric_context": n_numeric_context,
        "quantiles": args.quantiles if args.enable_uncertainty else [],
    }
    if args.model == "patchtst":
        return PatchTST(
            **common,
            patch_len=args.patch_len,
            patch_stride=args.patch_stride,
        )
    if args.model == "itransformer":
        return ITransformer(seq_len=args.seq_len, **common)
    if args.model == "heterostgnn":
        return HeteroSTGNNLoadNet(
            seq_len=args.seq_len,
            graph_layers=args.graph_layers,
            **common,
        )
    raise ValueError(f"Unsupported model: {args.model}")


def _model_version(model_name: str) -> str:
    if model_name == "heterostgnn":
        return "heterostgnn_v0.5_uncertainty"
    return f"{model_name}_v0.3_heterogeneous"


def _evaluate_predictions(y_true: np.ndarray, y_pred: np.ndarray, horizons: List[int], horizon_steps: Dict[int, int], target_columns: List[str]):
    metrics = {}
    for horizon in horizons:
        idx = horizon_steps[horizon] - 1
        if idx >= y_true.shape[1]:
            continue
        for col_idx, target in enumerate(target_columns):
            key = f"{target}_h{horizon}"
            metrics[key] = {
                k: round(v, 4) if v is not None and not np.isnan(v) else None
                for k, v in evaluator.evaluate_all(y_true[:, idx, col_idx], y_pred[:, idx, col_idx]).items()
            }
    return metrics


def _pinball_loss(torch, y_quantile, y_true, quantiles: List[float]):
    q = torch.tensor(quantiles, dtype=y_quantile.dtype, device=y_quantile.device).view(1, 1, 1, -1)
    err = y_true.unsqueeze(-1) - y_quantile
    return torch.maximum(q * err, (q - 1.0) * err).mean()


def _smoothness_loss(torch, y_point):
    if y_point.shape[1] < 3:
        return y_point.new_tensor(0.0)
    second_diff = y_point[:, 2:, :] - 2.0 * y_point[:, 1:-1, :] + y_point[:, :-2, :]
    return torch.mean(second_diff.pow(2))


def _power_constraint_loss(torch, y_point, target_columns: List[str], scaler, target_indices: List[int], power_limit: float):
    if power_limit <= 0 or "node_power" not in target_columns:
        return y_point.new_tensor(0.0)
    power_pos = target_columns.index("node_power")
    feature_idx = target_indices[power_pos]
    mean = torch.tensor(float(scaler.mean[feature_idx]), dtype=y_point.dtype, device=y_point.device)
    std = torch.tensor(float(scaler.std[feature_idx]), dtype=y_point.dtype, device=y_point.device)
    power = y_point[:, :, power_pos] * std + mean
    overflow = torch.relu(power - float(power_limit))
    return torch.mean((overflow / max(float(power_limit), 1.0)).pow(2))


def _extract_point(output):
    return output["point"] if isinstance(output, dict) else output


def _combined_loss(torch, criterion, output, y_true, args, target_columns, scaler, target_indices, power_limit):
    point = _extract_point(output)
    loss = criterion(point, y_true)
    if args.enable_uncertainty and isinstance(output, dict) and "quantiles" in output:
        loss = loss + args.quantile_loss_weight * _pinball_loss(torch, output["quantiles"], y_true, args.quantiles)
    if args.power_constraint_weight > 0:
        loss = loss + args.power_constraint_weight * _power_constraint_loss(
            torch,
            point,
            target_columns,
            scaler,
            target_indices,
            power_limit,
        )
    if args.smooth_loss_weight > 0:
        loss = loss + args.smooth_loss_weight * _smoothness_loss(torch, point)
    return loss


def _collect_outputs(torch, model, loader, device, enable_uncertainty: bool):
    preds_scaled = []
    true_scaled = []
    quantiles_scaled = []
    model.eval()
    with torch.no_grad():
        for X_batch, y_batch, context_cat_batch, context_num_batch in loader:
            output = model(
                X_batch.to(device),
                context_cat_batch.to(device),
                context_num_batch.to(device),
                return_quantiles=enable_uncertainty,
            )
            preds_scaled.append(_extract_point(output).detach().cpu().numpy())
            true_scaled.append(y_batch.numpy())
            if enable_uncertainty and isinstance(output, dict) and "quantiles" in output:
                quantiles_scaled.append(output["quantiles"].detach().cpu().numpy())
    return (
        np.concatenate(preds_scaled, axis=0),
        np.concatenate(true_scaled, axis=0),
        np.concatenate(quantiles_scaled, axis=0) if quantiles_scaled else None,
    )


def _inverse_quantiles(q_scaled: np.ndarray, scaler, target_indices: List[int]) -> np.ndarray:
    target_mean = scaler.mean[list(target_indices)].reshape(1, 1, -1, 1)
    target_std = scaler.std[list(target_indices)].reshape(1, 1, -1, 1)
    return q_scaled * target_std + target_mean


def _calibrate_uncertainty(
    y_true: np.ndarray,
    y_pred: np.ndarray,
    q_pred: np.ndarray,
    horizons: List[int],
    horizon_steps: Dict[int, int],
    target_columns: List[str],
    quantiles: List[float],
):
    payload = {
        "quantiles": quantiles,
        "conformal_abs_error_p90": {},
        "coverage_p10_p90": {},
    }
    for horizon in horizons:
        idx = horizon_steps[horizon] - 1
        if idx >= y_true.shape[1]:
            continue
        for col_idx, target in enumerate(target_columns):
            key = f"{target}_h{horizon}"
            abs_err = np.abs(y_true[:, idx, col_idx] - y_pred[:, idx, col_idx])
            payload["conformal_abs_error_p90"][key] = float(np.nanquantile(abs_err, 0.9)) if len(abs_err) else 0.0
            if q_pred is not None and 0.1 in quantiles and 0.9 in quantiles:
                lo_idx = quantiles.index(0.1)
                hi_idx = quantiles.index(0.9)
                lower = q_pred[:, idx, col_idx, lo_idx]
                upper = q_pred[:, idx, col_idx, hi_idx]
                coverage = np.mean((y_true[:, idx, col_idx] >= lower) & (y_true[:, idx, col_idx] <= upper))
                payload["coverage_p10_p90"][key] = float(coverage)
    return payload


def _infer_power_limit(df, target_cols: List[str], args) -> float:
    if "node_power" not in target_cols:
        return 0.0
    if args.power_limit > 0:
        return float(args.power_limit)
    if "rated_power" in df.columns:
        vals = np.asarray(df["rated_power"], dtype=np.float32)
        vals = vals[np.isfinite(vals) & (vals > 0)]
        if len(vals):
            return float(np.nanmax(vals) * args.power_limit_ratio)
    return 800.0


def main():
    parser = argparse.ArgumentParser(description="Train heterogeneous PatchTST/iTransformer/Hetero-STGNN models")
    parser.add_argument("--model", choices=["patchtst", "itransformer", "heterostgnn", "hetero_stgnn"], default="patchtst")
    parser.add_argument("--cluster-uuid", required=True)
    parser.add_argument("--start-time", required=True)
    parser.add_argument("--end-time", required=True)
    parser.add_argument("--node-uuids", default=None, help="optional comma-separated node UUIDs")
    parser.add_argument("--horizons", default="15,30,60")
    parser.add_argument("--seq-len", type=int, default=60)
    parser.add_argument("--feature-cols", default=",".join(deep_dataset.DEFAULT_INPUT_FEATURES))
    parser.add_argument("--target-cols", default=",".join(deep_dataset.DEFAULT_TARGET_COLS))
    parser.add_argument("--resample-freq", default=config.DEFAULT_RESAMPLE_FREQ)
    parser.add_argument("--max-missing-ratio", type=float, default=config.DEFAULT_MAX_MISSING_RATIO)
    parser.add_argument("--stride", type=int, default=1)
    parser.add_argument("--epochs", type=int, default=30)
    parser.add_argument("--batch-size", type=int, default=64)
    parser.add_argument("--learning-rate", type=float, default=1e-3)
    parser.add_argument("--weight-decay", type=float, default=1e-4)
    parser.add_argument("--d-model", type=int, default=64)
    parser.add_argument("--n-heads", type=int, default=4)
    parser.add_argument("--e-layers", type=int, default=2)
    parser.add_argument("--d-ff", type=int, default=128)
    parser.add_argument("--dropout", type=float, default=0.1)
    parser.add_argument("--patch-len", type=int, default=16)
    parser.add_argument("--patch-stride", type=int, default=8)
    parser.add_argument("--graph-layers", type=int, default=2)
    parser.add_argument("--enable-uncertainty", action="store_true")
    parser.add_argument("--quantiles", default="0.1,0.5,0.9")
    parser.add_argument("--quantile-loss-weight", type=float, default=0.4)
    parser.add_argument("--power-constraint-weight", type=float, default=0.05)
    parser.add_argument("--smooth-loss-weight", type=float, default=0.02)
    parser.add_argument("--power-limit", type=float, default=0.0)
    parser.add_argument("--power-limit-ratio", type=float, default=0.85)
    parser.add_argument("--context-cat-cols", default=",".join(deep_dataset.DEFAULT_CONTEXT_CATEGORICAL_COLS))
    parser.add_argument("--context-num-cols", default=",".join(deep_dataset.DEFAULT_CONTEXT_NUMERIC_COLS))
    parser.add_argument("--no-context-features", action="store_true")
    parser.add_argument("--device", default="auto", choices=["auto", "cpu", "cuda", "mps"])
    args = parser.parse_args()
    args.model = _normalize_model_name(args.model)

    try:
        torch, nn, DataLoader, TensorDataset, PatchTST, ITransformer, HeteroSTGNNLoadNet = _import_torch()
    except RuntimeError as exc:
        print(f"[ERROR] {exc}", file=sys.stderr)
        sys.exit(1)

    node_uuids = _parse_list(args.node_uuids) if args.node_uuids else None
    horizons = _parse_list(args.horizons, int)
    args.quantiles = _parse_list(args.quantiles, float)
    feature_cols = _parse_list(args.feature_cols)
    target_cols = _parse_list(args.target_cols)
    context_cat_cols = [] if args.no_context_features else _parse_list(args.context_cat_cols)
    context_num_cols = [] if args.no_context_features else _parse_list(args.context_num_cols)
    horizon_steps = deep_dataset.horizons_to_steps(horizons, args.resample_freq)
    pred_len = max(horizon_steps.values())

    print(f"[INFO] Loading data for cluster {args.cluster_uuid}", file=sys.stderr)
    df = data_loader.load_metrics(
        cluster_uuid=args.cluster_uuid,
        start_time=args.start_time,
        end_time=args.end_time,
        node_uuids=node_uuids,
    )
    if df.empty:
        print("[ERROR] No data loaded from green_node_metrics", file=sys.stderr)
        sys.exit(1)
    power_limit = _infer_power_limit(df, target_cols, args)

    samples = deep_dataset.build_sequence_samples(
        df,
        seq_len=args.seq_len,
        pred_len=pred_len,
        feature_columns=feature_cols,
        target_columns=target_cols,
        resample_freq=args.resample_freq,
        max_missing_ratio=args.max_missing_ratio,
        stride=args.stride,
        context_categorical_columns=context_cat_cols,
        context_numeric_columns=context_num_cols,
    )
    if len(samples.X) < 50:
        print(f"[ERROR] Not enough sequence samples: {len(samples.X)}", file=sys.stderr)
        sys.exit(1)

    train_idx, valid_idx, test_idx = deep_dataset.chronological_split_indices(samples.sample_times)
    if len(train_idx) == 0 or len(valid_idx) == 0 or len(test_idx) == 0:
        print("[ERROR] Chronological split produced an empty train/valid/test set", file=sys.stderr)
        sys.exit(1)

    scaler = deep_dataset.TimeSeriesScaler.fit(samples.X[train_idx], samples.feature_columns)
    context_encoder = deep_dataset.ContextFeatureEncoder.fit(
        samples.context_cat[train_idx],
        samples.context_num[train_idx],
        samples.context_cat_columns,
        samples.context_num_columns,
    )
    X_scaled = scaler.transform_X(samples.X)
    y_scaled = scaler.transform_y(samples.y, samples.target_indices)
    context_cat_encoded = context_encoder.transform_cat(samples.context_cat)
    context_num_scaled = context_encoder.transform_num(samples.context_num)

    train_ds = TensorDataset(
        torch.from_numpy(X_scaled[train_idx]),
        torch.from_numpy(y_scaled[train_idx]),
        torch.from_numpy(context_cat_encoded[train_idx]),
        torch.from_numpy(context_num_scaled[train_idx]),
    )
    valid_ds = TensorDataset(
        torch.from_numpy(X_scaled[valid_idx]),
        torch.from_numpy(y_scaled[valid_idx]),
        torch.from_numpy(context_cat_encoded[valid_idx]),
        torch.from_numpy(context_num_scaled[valid_idx]),
    )
    test_ds = TensorDataset(
        torch.from_numpy(X_scaled[test_idx]),
        torch.from_numpy(y_scaled[test_idx]),
        torch.from_numpy(context_cat_encoded[test_idx]),
        torch.from_numpy(context_num_scaled[test_idx]),
    )

    train_loader = DataLoader(train_ds, batch_size=args.batch_size, shuffle=True)
    valid_loader = DataLoader(valid_ds, batch_size=args.batch_size, shuffle=False)
    test_loader = DataLoader(test_ds, batch_size=args.batch_size, shuffle=False)

    if args.device == "auto":
        if torch.cuda.is_available():
            device = torch.device("cuda")
        elif getattr(torch.backends, "mps", None) and torch.backends.mps.is_available():
            device = torch.device("mps")
        else:
            device = torch.device("cpu")
    else:
        device = torch.device(args.device)

    model = _make_model(
        args,
        n_features=len(samples.feature_columns),
        target_indices=samples.target_indices,
        pred_len=pred_len,
        model_classes=(PatchTST, ITransformer, HeteroSTGNNLoadNet),
        context_encoder=context_encoder,
    ).to(device)
    criterion = nn.SmoothL1Loss()
    optimizer = torch.optim.AdamW(model.parameters(), lr=args.learning_rate, weight_decay=args.weight_decay)

    best_valid = float("inf")
    best_state = None
    history = []

    for epoch in range(1, args.epochs + 1):
        model.train()
        train_losses = []
        for X_batch, y_batch, context_cat_batch, context_num_batch in train_loader:
            X_batch = X_batch.to(device)
            y_batch = y_batch.to(device)
            context_cat_batch = context_cat_batch.to(device)
            context_num_batch = context_num_batch.to(device)
            optimizer.zero_grad(set_to_none=True)
            output = model(
                X_batch,
                context_cat_batch,
                context_num_batch,
                return_quantiles=args.enable_uncertainty,
            )
            loss = _combined_loss(
                torch,
                criterion,
                output,
                y_batch,
                args,
                samples.target_columns,
                scaler,
                samples.target_indices,
                power_limit,
            )
            loss.backward()
            torch.nn.utils.clip_grad_norm_(model.parameters(), max_norm=1.0)
            optimizer.step()
            train_losses.append(float(loss.detach().cpu()))

        model.eval()
        valid_losses = []
        with torch.no_grad():
            for X_batch, y_batch, context_cat_batch, context_num_batch in valid_loader:
                X_batch = X_batch.to(device)
                y_batch = y_batch.to(device)
                context_cat_batch = context_cat_batch.to(device)
                context_num_batch = context_num_batch.to(device)
                output = model(
                    X_batch,
                    context_cat_batch,
                    context_num_batch,
                    return_quantiles=args.enable_uncertainty,
                )
                valid_losses.append(float(_combined_loss(
                    torch,
                    criterion,
                    output,
                    y_batch,
                    args,
                    samples.target_columns,
                    scaler,
                    samples.target_indices,
                    power_limit,
                ).detach().cpu()))

        train_loss = float(np.mean(train_losses))
        valid_loss = float(np.mean(valid_losses))
        history.append({"epoch": epoch, "train_loss": train_loss, "valid_loss": valid_loss})
        print(f"[INFO] epoch={epoch} train_loss={train_loss:.6f} valid_loss={valid_loss:.6f}", file=sys.stderr)

        if valid_loss < best_valid:
            best_valid = valid_loss
            best_state = {k: v.detach().cpu() for k, v in model.state_dict().items()}

    if best_state is not None:
        model.load_state_dict(best_state)

    pred_scaled, true_scaled, quantile_scaled = _collect_outputs(
        torch,
        model,
        test_loader,
        device,
        args.enable_uncertainty,
    )
    y_pred = scaler.inverse_y(pred_scaled, samples.target_indices)
    y_true = scaler.inverse_y(true_scaled, samples.target_indices)
    q_pred = _inverse_quantiles(quantile_scaled, scaler, samples.target_indices) if quantile_scaled is not None else None
    metrics = _evaluate_predictions(y_true, y_pred, horizons, horizon_steps, samples.target_columns)
    uncertainty_payload = _calibrate_uncertainty(
        y_true,
        y_pred,
        q_pred,
        horizons,
        horizon_steps,
        samples.target_columns,
        args.quantiles,
    ) if args.enable_uncertainty else {}

    model_path = model_registry.get_deep_model_path(args.model)
    torch.save(
        {
            "model_state": model.state_dict(),
            "model_name": args.model,
            "created_at": datetime.now().isoformat(),
        },
        model_path,
    )

    metadata = {
        "model_name": args.model,
        "model_version": _model_version(args.model),
        "created_at": datetime.now().isoformat(),
        "train_data_range": {"start": args.start_time, "end": args.end_time},
        "cluster_uuid": args.cluster_uuid,
        "node_uuids": sorted(set(samples.node_uuids)),
        "feature_columns": samples.feature_columns,
        "target_columns": samples.target_columns,
        "target_indices": samples.target_indices,
        "context_feature_config": {
            "enabled": not args.no_context_features,
            "categorical_columns": samples.context_cat_columns,
            "numeric_columns": samples.context_num_columns,
        },
        "context_encoder": context_encoder.to_metadata(),
        "horizons": horizons,
        "horizon_steps": horizon_steps,
        "seq_len": args.seq_len,
        "pred_len": pred_len,
        "resample_freq": args.resample_freq,
        "max_missing_ratio": args.max_missing_ratio,
        "stride": args.stride,
        "model_params": {
            "d_model": args.d_model,
            "n_heads": args.n_heads,
            "e_layers": args.e_layers,
            "d_ff": args.d_ff,
            "dropout": args.dropout,
            "patch_len": args.patch_len,
            "patch_stride": args.patch_stride,
            "graph_layers": args.graph_layers,
        },
        "uncertainty_config": {
            "enabled": args.enable_uncertainty,
            "quantiles": args.quantiles if args.enable_uncertainty else [],
            "quantile_loss_weight": args.quantile_loss_weight,
            "power_constraint_weight": args.power_constraint_weight,
            "smooth_loss_weight": args.smooth_loss_weight,
            "power_limit": power_limit,
            "power_limit_ratio": args.power_limit_ratio,
            "calibration": uncertainty_payload,
        },
        "graph_config": {
            "enabled": args.model == "heterostgnn",
            "node_types": ["node", "service", "task"],
            "edge_types": [
                "self",
                "node_to_service",
                "service_to_node",
                "task_to_node",
                "service_to_task",
                "task_to_service",
            ],
        },
        "scaler": scaler.to_metadata(),
        "metrics": metrics,
        "loss_history": history,
        "sample_counts": {
            "total": len(samples.X),
            "train": len(train_idx),
            "valid": len(valid_idx),
            "test": len(test_idx),
        },
        "model_file": str(model_path),
    }
    model_registry.save_deep_metadata(args.model, metadata)

    print(json.dumps(metadata, indent=2, ensure_ascii=False))


if __name__ == "__main__":
    main()
