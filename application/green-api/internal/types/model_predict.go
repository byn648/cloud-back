package types

// ModelPredictRequest 请求体
type ModelPredictRequest struct {
	ClusterUUID string   `json:"cluster_uuid"`
	NodeUUIDs   []string `json:"node_uuids"`
	Horizons    []int    `json:"horizons"`
	ModelName   string   `json:"model_name"`
}

// ModelMetricItem 预测指标值
type ModelMetricItem struct {
	CPUUtil    *float64 `json:"cpu_util"`
	GPUUtil    *float64 `json:"gpu_util"`
	GPUMemUtil *float64 `json:"gpu_mem_util"`
	NodePower  *float64 `json:"node_power"`
}

// ModelConfidenceInterval 预测置信区间
type ModelConfidenceInterval struct {
	Lower ModelMetricItem `json:"lower"`
	Upper ModelMetricItem `json:"upper"`
}

// ModelPredictionItem 单条预测结果
type ModelPredictionItem struct {
	Horizon            int                        `json:"horizon"`
	ForecastTime       string                     `json:"forecast_time"`
	Metrics            ModelMetricItem            `json:"metrics"`
	Quantiles          map[string]ModelMetricItem `json:"quantiles,omitempty"`
	ConfidenceInterval *ModelConfidenceInterval   `json:"confidence_interval,omitempty"`
	OverloadRisk       map[string]*float64        `json:"overload_risk,omitempty"`
	SLOViolationRisk   *float64                   `json:"slo_violation_risk,omitempty"`
	RiskLevel          string                     `json:"risk_level"`
}

// ModelNodeResult 单节点预测结果
type ModelNodeResult struct {
	NodeUUID    string                `json:"node_uuid"`
	Predictions []ModelPredictionItem `json:"predictions,omitempty"`
	Error       string                `json:"error,omitempty"`
}

// ModelPredictResponse 预测响应
type ModelPredictResponse struct {
	ClusterUUID  string            `json:"cluster_uuid"`
	Horizons     []int             `json:"horizons"`
	ModelName    string            `json:"model_name"`
	ModelVersion string            `json:"model_version"`
	Results      []ModelNodeResult `json:"results"`
}

// ModelHistoryRequest 历史数据查询请求
type ModelHistoryRequest struct {
	ClusterUUID string `json:"cluster_uuid"`
	NodeUUID    string `json:"node_uuid"`
	StartTime   string `json:"start_time"`
	EndTime     string `json:"end_time"`
}

// ModelHistoryPoint 单条历史数据
type ModelHistoryPoint struct {
	Time       string   `json:"time"`
	CPUUtil    *float64 `json:"cpu_util"`
	GPUUtil    *float64 `json:"gpu_util"`
	GPUMemUtil *float64 `json:"gpu_mem_util"`
	NodePower  *float64 `json:"node_power"`
}

// ModelHistoryResponse 历史数据响应
type ModelHistoryResponse struct {
	NodeUUID    string              `json:"node_uuid"`
	ClusterUUID string              `json:"cluster_uuid"`
	StartTime   string              `json:"start_time"`
	EndTime     string              `json:"end_time"`
	Metrics     []ModelHistoryPoint `json:"metrics"`
}

// ModelMetadataMetrics 单个目标的评估指标
type ModelMetadataMetrics struct {
	MAE  *float64 `json:"mae"`
	RMSE *float64 `json:"rmse"`
	MAPE *float64 `json:"mape"`
	R2   *float64 `json:"r2"`
}

// ModelMetadataResponse 模型元信息响应
type ModelMetadataResponse struct {
	ModelVersion  string                          `json:"model_version"`
	CreatedAt     string                          `json:"created_at"`
	Horizons      []int                           `json:"horizons"`
	TargetColumns []string                        `json:"target_columns"`
	Metrics       map[string]ModelMetadataMetrics `json:"metrics"`
	Warnings      []string                        `json:"warnings"`
}
