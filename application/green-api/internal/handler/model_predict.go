package handler

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/yanshicheng/kube-nova/application/green-api/internal/svc"
	"github.com/yanshicheng/kube-nova/application/green-api/internal/types"
)

// ModelPredictHandler POST /green/v1/model/predict
func ModelPredictHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req types.ModelPredictRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, fmt.Sprintf("invalid request body: %v", err), http.StatusBadRequest)
			return
		}

		if req.ClusterUUID == "" {
			http.Error(w, "cluster_uuid is required", http.StatusBadRequest)
			return
		}
		if len(req.NodeUUIDs) == 0 {
			http.Error(w, "node_uuids is required and cannot be empty", http.StatusBadRequest)
			return
		}
		if len(req.Horizons) == 0 {
			req.Horizons = []int{15, 30, 60}
		}
		modelName := normalizeModelName(req.ModelName)

		horizonsStr := intSliceToCommaString(req.Horizons)
		resolvedNodeUUIDs := make([]string, 0, len(req.NodeUUIDs))
		for _, nodeUUID := range req.NodeUUIDs {
			resolvedNodeUUIDs = append(resolvedNodeUUIDs, resolveNodeUUID(svcCtx, req.ClusterUUID, nodeUUID))
		}
		nodeUUIDsStr := strings.Join(resolvedNodeUUIDs, ",")

		modelServiceDir := getModelServiceDir()
		predictScript := filepath.Join(modelServiceDir, "predict.py")
		if isDeepModel(modelName) {
			predictScript = filepath.Join(modelServiceDir, "predict_deep.py")
		}

		if _, err := os.Stat(predictScript); os.IsNotExist(err) {
			http.Error(w, fmt.Sprintf("%s not found, please train models first", filepath.Base(predictScript)), http.StatusServiceUnavailable)
			return
		}

		pythonBin := getPythonBin()
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()

		args := []string{
			predictScript,
			"--cluster-uuid", req.ClusterUUID,
			"--node-uuids", nodeUUIDsStr,
			"--horizons", horizonsStr,
		}
		if isDeepModel(modelName) {
			args = append(args, "--model", modelName)
		}
		cmd := exec.CommandContext(ctx, pythonBin, args...)
		cmd.Dir = modelServiceDir

		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		err := cmd.Run()

		if ctx.Err() == context.DeadlineExceeded {
			http.Error(w, "prediction timeout (60s), please try again", http.StatusGatewayTimeout)
			return
		}

		if err != nil {
			errMsg := strings.TrimSpace(stderr.String())
			if errMsg == "" {
				errMsg = err.Error()
			}
			http.Error(w, fmt.Sprintf("prediction failed: %s", errMsg), http.StatusInternalServerError)
			return
		}

		var pyResp struct {
			ClusterUUID  string `json:"cluster_uuid"`
			Horizons     []int  `json:"horizons"`
			ModelName    string `json:"model_name"`
			ModelVersion string `json:"model_version"`
			Error        string `json:"error,omitempty"`
			Results      []struct {
				NodeUUID    string `json:"node_uuid"`
				Error       string `json:"error,omitempty"`
				Predictions []struct {
					Horizon            int                            `json:"horizon"`
					ForecastTime       string                         `json:"forecast_time"`
					Metrics            map[string]*float64            `json:"metrics"`
					Quantiles          map[string]map[string]*float64 `json:"quantiles,omitempty"`
					ConfidenceInterval *struct {
						Lower map[string]*float64 `json:"lower"`
						Upper map[string]*float64 `json:"upper"`
					} `json:"confidence_interval,omitempty"`
					OverloadRisk     map[string]*float64 `json:"overload_risk,omitempty"`
					SLOViolationRisk *float64            `json:"slo_violation_risk,omitempty"`
					RiskLevel        string              `json:"risk_level"`
				} `json:"predictions,omitempty"`
			} `json:"results"`
		}

		if err := json.Unmarshal(stdout.Bytes(), &pyResp); err != nil {
			http.Error(w, fmt.Sprintf("failed to parse prediction result: %v, stderr: %s", err, stderr.String()), http.StatusInternalServerError)
			return
		}

		if pyResp.Error != "" {
			http.Error(w, fmt.Sprintf("prediction error: %s", pyResp.Error), http.StatusInternalServerError)
			return
		}

		resp := types.ModelPredictResponse{
			ClusterUUID:  req.ClusterUUID,
			Horizons:     req.Horizons,
			ModelName:    modelName,
			ModelVersion: pyResp.ModelVersion,
			Results:      make([]types.ModelNodeResult, 0, len(pyResp.Results)),
		}
		if pyResp.ModelName != "" {
			resp.ModelName = pyResp.ModelName
		}

		for _, nodeResult := range pyResp.Results {
			nr := types.ModelNodeResult{NodeUUID: nodeResult.NodeUUID}
			if nodeResult.Error != "" {
				nr.Error = nodeResult.Error
			} else {
				preds := make([]types.ModelPredictionItem, 0, len(nodeResult.Predictions))
				for _, p := range nodeResult.Predictions {
					item := types.ModelPredictionItem{
						Horizon:      p.Horizon,
						ForecastTime: p.ForecastTime,
						Metrics:      modelMetricItemFromMap(p.Metrics),
						RiskLevel:    p.RiskLevel,
					}
					if len(p.Quantiles) > 0 {
						item.Quantiles = make(map[string]types.ModelMetricItem, len(p.Quantiles))
						for label, metricMap := range p.Quantiles {
							item.Quantiles[label] = modelMetricItemFromMap(metricMap)
						}
					}
					if p.ConfidenceInterval != nil {
						item.ConfidenceInterval = &types.ModelConfidenceInterval{
							Lower: modelMetricItemFromMap(p.ConfidenceInterval.Lower),
							Upper: modelMetricItemFromMap(p.ConfidenceInterval.Upper),
						}
					}
					if len(p.OverloadRisk) > 0 {
						item.OverloadRisk = p.OverloadRisk
					}
					item.SLOViolationRisk = p.SLOViolationRisk
					preds = append(preds, item)
				}
				nr.Predictions = preds
			}
			resp.Results = append(resp.Results, nr)

			// upsert prediction results into green_model_prediction table
			savePredictionResults(svcCtx, req.ClusterUUID, resp.ModelName, pyResp.ModelVersion, nr)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(resp)
	}
}

func modelMetricItemFromMap(metrics map[string]*float64) types.ModelMetricItem {
	return types.ModelMetricItem{
		CPUUtil:    metrics["cpu_util"],
		GPUUtil:    metrics["gpu_util"],
		GPUMemUtil: metrics["gpu_mem_util"],
		NodePower:  metrics["node_power"],
	}
}

// savePredictionResults upserts prediction results into green_model_prediction table
func savePredictionResults(svcCtx *svc.ServiceContext, clusterUUID, modelName, modelVersion string, nodeResult types.ModelNodeResult) {
	if len(nodeResult.Predictions) == 0 {
		return
	}

	for _, pred := range nodeResult.Predictions {
		for metricName, metricVal := range map[string]*float64{
			"cpu_util":     pred.Metrics.CPUUtil,
			"gpu_util":     pred.Metrics.GPUUtil,
			"gpu_mem_util": pred.Metrics.GPUMemUtil,
			"node_power":   pred.Metrics.NodePower,
		} {
			if metricVal == nil {
				continue
			}
			_, err := svcCtx.GreenRepo.Exec(`
				INSERT INTO green_model_prediction
					(cluster_uuid, node_uuid, target_metric, forecast_time, horizon, y_pred, risk_level, model_name, model_version, created_at)
				VALUES
					(?, ?, ?, ?, ?, ?, ?, ?, ?, NOW())
				ON DUPLICATE KEY UPDATE
					y_pred = VALUES(y_pred),
					risk_level = VALUES(risk_level),
					model_name = VALUES(model_name),
					model_version = VALUES(model_version),
					created_at = NOW()
			`, clusterUUID, nodeResult.NodeUUID, metricName, pred.ForecastTime,
				pred.Horizon, *metricVal, pred.RiskLevel, modelName, modelVersion)
			if err != nil {
				// log but don't fail the request
				fmt.Fprintf(os.Stderr, "[WARN] Failed to upsert prediction result: %v\n", err)
			}
		}
	}
}

func normalizeModelName(modelName string) string {
	switch strings.ToLower(strings.TrimSpace(modelName)) {
	case "", "lightgbm", "baseline":
		return "lightgbm"
	case "patchtst":
		return "patchtst"
	case "itransformer", "i-transformer":
		return "itransformer"
	case "heterostgnn", "hetero-stgnn", "hetero_stgnn", "hetero-stgnn-loadnet", "hetero_stgnn_loadnet":
		return "heterostgnn"
	default:
		return "lightgbm"
	}
}

func isDeepModel(modelName string) bool {
	switch modelName {
	case "patchtst", "itransformer", "heterostgnn":
		return true
	default:
		return false
	}
}

// ModelHistoryHandler GET /green/v1/model/history
func ModelHistoryHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		clusterUUID := r.URL.Query().Get("cluster_uuid")
		nodeUUID := r.URL.Query().Get("node_uuid")
		startTime := r.URL.Query().Get("start_time")
		endTime := r.URL.Query().Get("end_time")

		if clusterUUID == "" || nodeUUID == "" {
			http.Error(w, "cluster_uuid and node_uuid are required", http.StatusBadRequest)
			return
		}
		nodeUUID = resolveNodeUUID(svcCtx, clusterUUID, nodeUUID)
		if startTime == "" || endTime == "" {
			latest, ok := latestMetricTime(svcCtx, clusterUUID, nodeUUID)
			if !ok {
				latest = time.Now()
			}
			if endTime == "" {
				endTime = latest.Format("2006-01-02 15:04:05")
			}
			if startTime == "" {
				startTime = latest.Add(-2 * time.Hour).Format("2006-01-02 15:04:05")
			}
		}

		rows, err := svcCtx.GreenRepo.Query(`
			SELECT metric_time,
				COALESCE(cpu_usage, NULL) AS cpu_util,
				COALESCE(gpu_usage, NULL) AS gpu_util,
				COALESCE(gpu_memory_usage, NULL) AS gpu_mem_util,
				COALESCE(node_power, NULL) AS node_power
			FROM green_node_metrics
			WHERE cluster_uuid = ? AND node_uuid = ?
				AND metric_time >= ? AND metric_time <= ?
			ORDER BY metric_time ASC
			LIMIT 2000
		`, clusterUUID, nodeUUID, startTime, endTime)
		if err != nil {
			http.Error(w, fmt.Sprintf("failed to query history: %v", err), http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		points := make([]types.ModelHistoryPoint, 0)
		for rows.Next() {
			var metricTime string
			var cpuUtil, gpuUtil, gpuMemUtil, nodePower *float64
			if err := rows.Scan(&metricTime, &cpuUtil, &gpuUtil, &gpuMemUtil, &nodePower); err != nil {
				continue
			}
			points = append(points, types.ModelHistoryPoint{
				Time:       metricTime,
				CPUUtil:    cpuUtil,
				GPUUtil:    gpuUtil,
				GPUMemUtil: gpuMemUtil,
				NodePower:  nodePower,
			})
		}

		resp := types.ModelHistoryResponse{
			NodeUUID:    nodeUUID,
			ClusterUUID: clusterUUID,
			StartTime:   startTime,
			EndTime:     endTime,
			Metrics:     points,
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(resp)
	}
}

func resolveNodeUUID(svcCtx *svc.ServiceContext, clusterUUID, requestedNodeUUID string) string {
	if nodeHasMetrics(svcCtx, clusterUUID, requestedNodeUUID) {
		return requestedNodeUUID
	}

	rows, err := svcCtx.GreenRepo.Query(`
		SELECT node_uuid
		FROM green_node_metrics
		WHERE cluster_uuid = ?
		GROUP BY node_uuid
		ORDER BY MAX(metric_time) DESC
		LIMIT 1
	`, clusterUUID)
	if err != nil {
		return requestedNodeUUID
	}
	defer rows.Close()

	if rows.Next() {
		var nodeUUID string
		if err := rows.Scan(&nodeUUID); err == nil && nodeUUID != "" {
			return nodeUUID
		}
	}
	return requestedNodeUUID
}

func nodeHasMetrics(svcCtx *svc.ServiceContext, clusterUUID, nodeUUID string) bool {
	rows, err := svcCtx.GreenRepo.Query(`
		SELECT 1
		FROM green_node_metrics
		WHERE cluster_uuid = ? AND node_uuid = ?
		LIMIT 1
	`, clusterUUID, nodeUUID)
	if err != nil {
		return false
	}
	defer rows.Close()
	return rows.Next()
}

func latestMetricTime(svcCtx *svc.ServiceContext, clusterUUID, nodeUUID string) (time.Time, bool) {
	rows, err := svcCtx.GreenRepo.Query(`
		SELECT MAX(metric_time)
		FROM green_node_metrics
		WHERE cluster_uuid = ? AND node_uuid = ?
	`, clusterUUID, nodeUUID)
	if err != nil {
		return time.Time{}, false
	}
	defer rows.Close()

	if !rows.Next() {
		return time.Time{}, false
	}

	var latest sql.NullTime
	if err := rows.Scan(&latest); err != nil || !latest.Valid {
		return time.Time{}, false
	}
	return latest.Time, true
}

// ModelMetadataHandler GET /green/v1/model/metadata
func ModelMetadataHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		modelServiceDir := getModelServiceDir()
		metaPath := filepath.Join(modelServiceDir, "checkpoints", "metadata.json")

		data, err := os.ReadFile(metaPath)
		if err != nil {
			http.Error(w, fmt.Sprintf("metadata not found: %v", err), http.StatusNotFound)
			return
		}

		var rawMeta struct {
			ModelVersion string   `json:"model_version"`
			CreatedAt    string   `json:"created_at"`
			Horizons     []int    `json:"horizons"`
			TargetCols   []string `json:"target_columns"`
			Metrics      map[string]struct {
				MAE  *float64 `json:"mae"`
				RMSE *float64 `json:"rmse"`
				MAPE *float64 `json:"mape"`
				R2   *float64 `json:"r2"`
			} `json:"metrics"`
			Warnings []string `json:"warnings"`
		}

		if err := json.Unmarshal(data, &rawMeta); err != nil {
			http.Error(w, fmt.Sprintf("failed to parse metadata: %v", err), http.StatusInternalServerError)
			return
		}

		formattedMetrics := make(map[string]types.ModelMetadataMetrics)
		for k, v := range rawMeta.Metrics {
			formattedMetrics[k] = types.ModelMetadataMetrics{
				MAE:  v.MAE,
				RMSE: v.RMSE,
				MAPE: v.MAPE,
				R2:   v.R2,
			}
		}

		resp := types.ModelMetadataResponse{
			ModelVersion:  rawMeta.ModelVersion,
			CreatedAt:     rawMeta.CreatedAt,
			Horizons:      rawMeta.Horizons,
			TargetColumns: rawMeta.TargetCols,
			Metrics:       formattedMetrics,
			Warnings:      rawMeta.Warnings,
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(resp)
	}
}

func intSliceToCommaString(arr []int) string {
	strs := make([]string, len(arr))
	for i, v := range arr {
		strs[i] = strconv.Itoa(v)
	}
	return strings.Join(strs, ",")
}

func getModelServiceDir() string {
	if dir := os.Getenv("MODEL_SERVICE_DIR"); dir != "" {
		return dir
	}
	if wd, err := os.Getwd(); err == nil {
		candidates := []string{
			filepath.Join(wd, "model_service"),
			filepath.Join(wd, "..", "..", "model_service"),
		}
		for _, candidate := range candidates {
			if info, err := os.Stat(candidate); err == nil && info.IsDir() {
				return candidate
			}
		}
	}
	return "/home/wph/beiyou_new/cloud-back/model_service"
}

func getPythonBin() string {
	if bin := os.Getenv("PYTHON_BIN"); bin != "" {
		return bin
	}
	if _, err := os.Stat("/home/wph/.conda/envs/yun/bin/python"); err == nil {
		return "/home/wph/.conda/envs/yun/bin/python"
	}
	return "python3"
}
