# Model Service

## 课题二：端到端 SLO 感知资源调度系统演变

课题二按“感知 -> 分解 -> 预测 -> 调度 -> 依赖优化 -> 功率安全”的闭环逐步演进，每个阶段都向下一阶段提供更具体的决策输入。

**当前进度：已实现到 V0.3 SLO 代理模型阶段。** 目前系统已经具备 V0.1 SLO 状态感知、V0.2 端到端 SLO 到服务级 budget 分解、V0.3 LightGBM 工程基线、GRU/TCN sequence model 和 GNN-based predictor 的资源配置到 P95/P99/违约概率预测能力。下一阶段可进入 V0.4 智能调度。

| 阶段 | 能力演变 | 已落地内容 |
| --- | --- | --- |
| V0.1 SLO 监控与违约识别 | 从“只看资源指标”升级为“能判断服务 SLO 状态”。输入 QPS、P95/P99、错误率、副本数、CPU/GPU/功耗；输出 NORMAL/WARNING/VIOLATED 和违约风险。 | `slo_metric_ts`、`slo_status`、`slo_v01.py train/score/seed-demo`，后端 `/performance/slo` 返回状态汇总、服务级状态和风险趋势。 |
| V0.2 端到端 SLO 分解 | 从“知道是否违约”升级为“知道每个服务应该承担多少 SLO budget”。端到端 P99/错误率按服务历史贡献、流量波动、错误率和关键路径拆成服务级预算。 | `slo_e2e_objective`、`slo_service_dependency`、`slo_budget_allocation`，后端实现 baseline/risk-weighted 分配并在页面展示“端到端 SLO 分解”。 |
| V0.3 SLO 代理模型 | 从“服务级预算”升级为“预测资源配置是否能满足预算”。输入未来负载、CPU/GPU/内存/副本配置、历史延迟序列、V0.2 budget 和服务依赖图；输出未来 P95/P99 与违约概率。 | `slo_proxy_prediction`、`slo_proxy_v03.py train/predict`，已支持 `--model lgbm/gru/tcn/gnn`。GRU/TCN/GNN checkpoint 分别保存为 `slo_proxy_v03_gru.pt`、`slo_proxy_v03_tcn.pt`、`slo_proxy_v03_gnn.pt`，后端返回 `proxyPrediction`，页面展示每个服务 15/30/60min 的资源配置、P99 预测和违约概率。 |
| V0.4 智能调度 | 从“预测风险”升级为“根据预测选择资源配置”。调度器用 V0.3 代理模型评估候选 CPU/GPU/副本方案，选择满足 SLO 且资源成本较低的方案。 | 后续接入调度决策表与推荐接口。 |
| V0.5 服务依赖优化 | 从“单服务预测”升级为“调用链联合优化”。结合服务依赖图和关键路径，避免局部扩容不能改善端到端 SLO 的情况。 | 后续在 V0.2 依赖图基础上引入图约束或 GNN predictor。 |
| V0.6 功率与储能安全约束 | 从“性能优先调度”升级为“SLO、能耗、功率安全联合约束”。在调度时加入节点功率上限、储能状态、备电能力和安全阈值。 | 后续与现有功率/储能监控和预测模块联动。 |

## 课题二 V0.1-V0.3 演示命令

Create the V0.1/V0.2/V0.3 SLO tables in an existing database:

```bash
mysql -h 127.0.0.1 -P 13308 -uroot -p123456 --protocol=tcp cloud_back < sql/slo_v01.sql
mysql -h 127.0.0.1 -P 13308 -uroot -p123456 --protocol=tcp cloud_back < sql/slo_v02.sql
mysql -h 127.0.0.1 -P 13308 -uroot -p123456 --protocol=tcp cloud_back < sql/slo_v03.sql
```

Seed demo SLO metric/status data:

```bash
python3 -m model_service.slo_v01 seed-demo \
  --hours 48 \
  --interval-minutes 5 \
  --write-status
```

Train the optional V0.1 LightGBM SLO status classifier from `slo_metric_ts`:

```bash
python3 -m model_service.slo_v01 train \
  --start-time "2026-05-19 00:00:00" \
  --end-time "2026-05-24 23:59:59"
```

Score the latest SLO samples and write `slo_status`:

```bash
python3 -m model_service.slo_v01 score \
  --limit 200 \
  --write-status
```

Train the V0.3 LightGBM proxy baseline from `slo_metric_ts + slo_budget_allocation`:

```bash
python3 -m model_service.slo_proxy_v03 train \
  --model lgbm \
  --start-time "2026-05-19 00:00:00" \
  --end-time "2026-05-24 23:59:59" \
  --horizons 15,30,60
```

Train the V0.3 GRU sequence model:

```bash
python3 -m model_service.slo_proxy_v03 train \
  --model gru \
  --start-time "2026-05-19 00:00:00" \
  --end-time "2026-05-24 23:59:59" \
  --horizons 15,30,60 \
  --seq-len 24 \
  --epochs 30 \
  --batch-size 64 \
  --device auto
```

Train the V0.3 TCN sequence model:

```bash
python3 -m model_service.slo_proxy_v03 train \
  --model tcn \
  --start-time "2026-05-19 00:00:00" \
  --end-time "2026-05-24 23:59:59" \
  --horizons 15,30,60 \
  --seq-len 24 \
  --epochs 30 \
  --batch-size 64 \
  --device auto
```

Train the V0.3 GNN-based SLO predictor:

```bash
python3 -m model_service.slo_proxy_v03 train \
  --model gnn \
  --start-time "2026-05-19 00:00:00" \
  --end-time "2026-05-24 23:59:59" \
  --horizons 15,30,60 \
  --seq-len 24 \
  --graph-layers 2 \
  --epochs 30 \
  --batch-size 64 \
  --device auto
```

Predict service-level latency/risk under resource configs and write `slo_proxy_prediction`:

```bash
python3 -m model_service.slo_proxy_v03 predict \
  --model auto \
  --horizons 15,30,60 \
  --write-predictions
```

Use a trained GRU/TCN checkpoint for prediction:

```bash
python3 -m model_service.slo_proxy_v03 predict \
  --model gru \
  --horizons 15,30,60 \
  --write-predictions
```

Use a trained GNN checkpoint for prediction:

```bash
python3 -m model_service.slo_proxy_v03 predict \
  --model gnn \
  --horizons 15,30,60 \
  --write-predictions \
  --device auto
```

## V0.1 LightGBM

```bash
python3 -m model_service.train_baseline \
  --cluster-uuid <cluster_uuid> \
  --start-time "2026-03-21 20:21:21" \
  --end-time "2026-05-19 09:50:04" \
  --horizons 15,30,60 \
  --history-window 60
```

If a raw collector run has not been imported yet, convert it into the Topic 1
training tables first. The default mode is read-only:

```bash
python3 -m model_service.import_raw_run \
  --run-dir /home/wph/beiyou_new/pod_raw_runs/run_20260527_134101 \
  --cluster-uuid run_20260527_134101 \
  --output-dir /tmp/run_20260527_134101_preview
```

Write the normalized rows into MySQL after checking the summary:

```bash
python3 -m model_service.import_raw_run \
  --run-dir /home/wph/beiyou_new/pod_raw_runs/run_20260527_134101 \
  --cluster-uuid run_20260527_134101 \
  --write \
  --replace
```

For short smoke-test datasets where `node_power_w` is unavailable, skip
`node_power` targets:

```bash
python3 -m model_service.train_baseline \
  --cluster-uuid run_20260527_134101 \
  --start-time "2026-05-27 13:41:11" \
  --end-time "2026-05-27 15:56:48" \
  --horizons 15,30 \
  --history-window 20 \
  --target-cols cpu_util,gpu_util,gpu_mem_util
```

## V0.3 Heterogeneous PatchTST / iTransformer

Stage 3 trains directly on raw multivariate time series from `green_node_metrics`
and fuses hardware/deployment context into the time-series backbone:

- categorical embeddings: `cpu_model`, `gpu_model`, `node_type`, `task_type`, `schedule_policy`
- numeric MLP: `gpu_count`, `gpu_mem_gb`, `rated_power`, `replicas`, `requests`, `limits`, `batch_size`, `concurrency`, `request_rate`

Missing columns are filled with unknown/zero values, so the same commands also work
with older schemas. It does not require a pretrained model. Install PyTorch before
training:

```bash
python3 -m pip install torch
```

Train PatchTST:

```bash
python3 -m model_service.train_deep \
  --model patchtst \
  --cluster-uuid <cluster_uuid> \
  --start-time "2026-03-21 20:21:21" \
  --end-time "2026-05-19 09:50:04" \
  --horizons 15,30,60 \
  --seq-len 60 \
  --epochs 30
```

Short smoke test without `node_power`:

```bash
python3 -m model_service.train_deep \
  --model patchtst \
  --cluster-uuid run_20260527_134101 \
  --start-time "2026-05-27 13:41:11" \
  --end-time "2026-05-27 15:56:48" \
  --horizons 15,30 \
  --seq-len 20 \
  --epochs 3 \
  --feature-cols cpu_util,gpu_util,gpu_mem_util,mem_util,disk_io,net_io \
  --target-cols cpu_util,gpu_util,gpu_mem_util
```

To ablate heterogeneous context features:

```bash
python3 -m model_service.train_deep \
  --model patchtst \
  --cluster-uuid <cluster_uuid> \
  --start-time "2026-03-21 20:21:21" \
  --end-time "2026-05-19 09:50:04" \
  --horizons 15,30,60 \
  --seq-len 60 \
  --epochs 30 \
  --no-context-features
```

Train iTransformer:

```bash
python3 -m model_service.train_deep \
  --model itransformer \
  --cluster-uuid <cluster_uuid> \
  --start-time "2026-03-21 20:21:21" \
  --end-time "2026-05-19 09:50:04" \
  --horizons 15,30,60 \
  --seq-len 60 \
  --epochs 30
```

Predict with a trained deep model:

```bash
python3 model_service/predict_deep.py \
  --model patchtst \
  --cluster-uuid <cluster_uuid> \
  --node-uuids <node_uuid> \
  --horizons 15,30,60
```

Generated artifacts:

- `checkpoints/deep_patchtst.pt`
- `checkpoints/deep_patchtst_metadata.json`
- `checkpoints/deep_itransformer.pt`
- `checkpoints/deep_itransformer_metadata.json`

## V0.4 Hetero-STGNN-LoadNet

Stage 4 extends the stage-3 heterogeneous time-series backbone with an explicit
node-service-task graph block. The implemented model is named `heterostgnn`
(`Hetero-STGNN-LoadNet`, 异构资源感知时空图负荷预测网络).

Current graph abstraction:

- node token: multivariate temporal state from CPU/GPU/GPU memory/power history
- service token: deployment/service context from task type, scheduling policy, replicas, requests and limits
- task token: task context from task type, batch size, concurrency and request rate
- edge-typed message passing: self edges, node-service deployment edges, task-node running edges, and service-task interaction edges

The present collector does not yet provide explicit `service_id`, `task_id` or
service-service call-chain edges. Therefore V0.4 uses the available node metrics
plus static/semi-static context to build a compact per-sample heterogeneous
graph. When explicit service/task/dependency data is collected later, the same
model entry point can be extended to use real graph edges.

Train Hetero-STGNN:

```bash
python3 -m model_service.train_deep \
  --model heterostgnn \
  --cluster-uuid <cluster_uuid> \
  --start-time "2026-03-21 20:21:21" \
  --end-time "2026-05-19 09:50:04" \
  --horizons 15,30,60 \
  --seq-len 60 \
  --graph-layers 2 \
  --epochs 30
```

Short smoke test without `node_power`:

```bash
python3 -m model_service.train_deep \
  --model heterostgnn \
  --cluster-uuid run_20260527_134101 \
  --start-time "2026-05-27 13:41:11" \
  --end-time "2026-05-27 15:56:48" \
  --horizons 15,30 \
  --seq-len 20 \
  --graph-layers 2 \
  --epochs 3 \
  --feature-cols cpu_util,gpu_util,gpu_mem_util,mem_util,disk_io,net_io \
  --target-cols cpu_util,gpu_util,gpu_mem_util
```

Predict with Hetero-STGNN:

```bash
python3 model_service/predict_deep.py \
  --model heterostgnn \
  --cluster-uuid <cluster_uuid> \
  --node-uuids <node_uuid> \
  --horizons 15,30,60
```

Generated artifacts:

- `checkpoints/deep_heterostgnn.pt`
- `checkpoints/deep_heterostgnn_metadata.json`

## V0.5 Uncertainty Prediction and Risk Output

Stage 5 adds uncertainty-aware outputs for scheduling and SLO control. Deep
models can now train an optional quantile head and use a composite loss:

`L = L_point + λ1 L_quantile + λ2 L_power_constraint + λ3 L_smooth`

Outputs are backward compatible with earlier stages. Existing `metrics` remains
the point prediction, and uncertainty-aware checkpoints additionally return:

- `quantiles`: `p10`, `p50`, `p90`
- `confidence_interval`: lower/upper interval after conformal residual expansion
- `overload_risk`: per-metric overload probability estimate
- `slo_violation_risk`: max risk across predicted resource metrics
- `risk_level`: LOW / MEDIUM / HIGH derived from risk probability

Train the stage-5 Hetero-STGNN uncertainty model:

```bash
python3 -m model_service.train_deep \
  --model heterostgnn \
  --cluster-uuid <cluster_uuid> \
  --start-time "2026-03-21 20:21:21" \
  --end-time "2026-05-19 09:50:04" \
  --horizons 15,30,60 \
  --seq-len 60 \
  --graph-layers 2 \
  --enable-uncertainty \
  --quantiles 0.1,0.5,0.9 \
  --quantile-loss-weight 0.4 \
  --power-constraint-weight 0.05 \
  --smooth-loss-weight 0.02 \
  --epochs 30
```

Short smoke test without `node_power`:

```bash
python3 -m model_service.train_deep \
  --model heterostgnn \
  --cluster-uuid run_20260527_134101 \
  --start-time "2026-05-27 13:41:11" \
  --end-time "2026-05-27 15:56:48" \
  --horizons 15,30 \
  --seq-len 20 \
  --graph-layers 2 \
  --enable-uncertainty \
  --epochs 3 \
  --feature-cols cpu_util,gpu_util,gpu_mem_util,mem_util,disk_io,net_io \
  --target-cols cpu_util,gpu_util,gpu_mem_util
```

Predict with uncertainty fields:

```bash
python3 model_service/predict_deep.py \
  --model heterostgnn \
  --cluster-uuid <cluster_uuid> \
  --node-uuids <node_uuid> \
  --horizons 15,30,60
```
