package types

// ===================== SLO =====================

// SLOMetric SLO指标项
type SLOMetric struct {
	Key    string  `json:"key"`
	Label  string  `json:"label"`
	Value  float64 `json:"value"`
	Unit   string  `json:"unit"`
	Color  string  `json:"color"`
	Target float64 `json:"target"`
	Status string  `json:"status"`
	Max    float64 `json:"max"`
}

// SLOTimePoint 48h趋势数据点
type SLOTimePoint struct {
	Timestamp      int64   `json:"timestamp"`
	APIGW          float64 `json:"apiGw"`
	ModelInference float64 `json:"modelInference"`
	DataProcess    float64 `json:"dataProcess"`
}

// SLOBudget SLO错误预算
type SLOBudget struct {
	TotalBudgetPercent   float64 `json:"totalBudgetPercent"`
	RemainingPercent     float64 `json:"remainingPercent"`
	BurnRate             float64 `json:"burnRate"`
	ProjectedExhaustDate string  `json:"projectedExhaustDate"`
	Status               string  `json:"status"`
}

// SLOStatusSummary V0.1 SLO状态汇总
type SLOStatusSummary struct {
	Status        string  `json:"status"`
	CurrentRisk   float64 `json:"currentRisk"`
	NormalCount   int     `json:"normalCount"`
	WarningCount  int     `json:"warningCount"`
	ViolatedCount int     `json:"violatedCount"`
	TotalCount    int     `json:"totalCount"`
	LastUpdated   int64   `json:"lastUpdated"`
}

// SLOServiceStatus V0.1 服务/API级SLO状态
type SLOServiceStatus struct {
	ClusterUuid   string  `json:"clusterUuid"`
	ServiceID     string  `json:"serviceId"`
	ServiceName   string  `json:"serviceName"`
	APIID         string  `json:"apiId"`
	Status        string  `json:"status"`
	ViolationRisk float64 `json:"violationRisk"`
	Reason        string  `json:"reason"`
	Timestamp     int64   `json:"timestamp"`
	QPS           float64 `json:"qps"`
	P95Latency    float64 `json:"p95Latency"`
	P99Latency    float64 `json:"p99Latency"`
	ErrorRate     float64 `json:"errorRate"`
	ReplicaCount  int     `json:"replicaCount"`
	CPUUtil       float64 `json:"cpuUtil"`
	GPUUtil       float64 `json:"gpuUtil"`
	NodePower     float64 `json:"nodePower"`
}

// SLOStatusTrendPoint V0.1 违约风险趋势点
type SLOStatusTrendPoint struct {
	Timestamp     int64   `json:"timestamp"`
	Risk          float64 `json:"risk"`
	NormalCount   int     `json:"normalCount"`
	WarningCount  int     `json:"warningCount"`
	ViolatedCount int     `json:"violatedCount"`
}

// SLOServiceBudget V0.2 服务级SLO预算
type SLOServiceBudget struct {
	ServiceID           string  `json:"serviceId"`
	ServiceName         string  `json:"serviceName"`
	APIID               string  `json:"apiId"`
	P99LatencyBudget    float64 `json:"p99LatencyBudget"`
	ErrorRateBudget     float64 `json:"errorRateBudget"`
	BudgetRatio         float64 `json:"budgetRatio"`
	LatencyContribution float64 `json:"latencyContribution"`
	QPS                 float64 `json:"qps"`
	QPSCV               float64 `json:"qpsCv"`
	ErrorRate           float64 `json:"errorRate"`
	CriticalPath        bool    `json:"criticalPath"`
	RiskWeight          float64 `json:"riskWeight"`
}

// SLOBudgetAllocation V0.2 端到端SLO分解结果
type SLOBudgetAllocation struct {
	ObjectiveID        string             `json:"objectiveId"`
	BusinessFlow       string             `json:"businessFlow"`
	Method             string             `json:"method"`
	TargetP99Latency   float64            `json:"targetP99Latency"`
	TargetErrorRate    float64            `json:"targetErrorRate"`
	AllocatedLatency   float64            `json:"allocatedLatency"`
	AllocatedErrorRate float64            `json:"allocatedErrorRate"`
	GeneratedAt        int64              `json:"generatedAt"`
	Services           []SLOServiceBudget `json:"services"`
}

// SLOProxyPrediction V0.3 SLO代理模型输出
type SLOProxyPrediction struct {
	ObjectiveID          string  `json:"objectiveId"`
	BusinessFlow         string  `json:"businessFlow"`
	ServiceID            string  `json:"serviceId"`
	ServiceName          string  `json:"serviceName"`
	APIID                string  `json:"apiId"`
	ForecastTime         int64   `json:"forecastTime"`
	HorizonMinutes       int     `json:"horizonMinutes"`
	QPSForecast          float64 `json:"qpsForecast"`
	RequestMix           string  `json:"requestMix"`
	ReplicaCount         int     `json:"replicaCount"`
	CPURequest           float64 `json:"cpuRequest"`
	GPURequest           float64 `json:"gpuRequest"`
	MemoryRequestGB      float64 `json:"memoryRequestGb"`
	PredictedCPUUtil     float64 `json:"predictedCpuUtil"`
	PredictedGPUUtil     float64 `json:"predictedGpuUtil"`
	P99LatencyBudget     float64 `json:"p99LatencyBudget"`
	ErrorRateBudget      float64 `json:"errorRateBudget"`
	PredictedP95Latency  float64 `json:"predictedP95Latency"`
	PredictedP99Latency  float64 `json:"predictedP99Latency"`
	ViolationProbability float64 `json:"violationProbability"`
	CanMeetBudget        bool    `json:"canMeetBudget"`
	ModelName            string  `json:"modelName"`
	ModelVersion         string  `json:"modelVersion"`
}

// SLOProxyResponse V0.3 SLO代理模型预测汇总
type SLOProxyResponse struct {
	ModelName    string               `json:"modelName"`
	ModelVersion string               `json:"modelVersion"`
	GeneratedAt  int64                `json:"generatedAt"`
	Predictions  []SLOProxyPrediction `json:"predictions"`
}

// SLOResponse SLO达标响应
type SLOResponse struct {
	Metrics          []SLOMetric           `json:"metrics"`
	Trend48h         []SLOTimePoint        `json:"trend48h"`
	SLOBudget        SLOBudget             `json:"sloBudget"`
	StatusSummary    SLOStatusSummary      `json:"statusSummary"`
	ServiceStatuses  []SLOServiceStatus    `json:"serviceStatuses"`
	StatusTrend48h   []SLOStatusTrendPoint `json:"statusTrend48h"`
	BudgetAllocation SLOBudgetAllocation   `json:"budgetAllocation"`
	ProxyPrediction  SLOProxyResponse      `json:"proxyPrediction"`
}

// ===================== Error Budget =====================

// DailyBurnRate 每日消耗率
type DailyBurnRate struct {
	Day      string  `json:"day"`
	Consumed float64 `json:"consumed"`
}

// ErrorBudgetResponse 错误预算响应
type ErrorBudgetResponse struct {
	TotalBudgetHours     float64         `json:"totalBudgetHours"`
	ConsumedHours        float64         `json:"consumedHours"`
	RemainingHours       float64         `json:"remainingHours"`
	BurnRate             float64         `json:"burnRate"`
	ProjectedExhaustDate string          `json:"projectedExhaustDate"`
	Status               string          `json:"status"`
	DailyBurnRate        float64         `json:"dailyBurnRate"`
	WeeklyTrend          []DailyBurnRate `json:"weeklyTrend"`
}

// ===================== Resource =====================

// ResourceItem 资源项
type ResourceItem struct {
	Label string  `json:"label"`
	Value float64 `json:"value"`
	Max   float64 `json:"max"`
	Unit  string  `json:"unit"`
	Color string  `json:"color"`
}

// PowerTrendPoint 功率趋势数据点
type PowerTrendPoint struct {
	Timestamp    int64   `json:"timestamp"`
	ComputePower float64 `json:"computePower"`
	StoragePower float64 `json:"storagePower"`
}

// ResourceResponse 资源管控响应
type ResourceResponse struct {
	Items         []ResourceItem    `json:"items"`
	PowerTrend48h []PowerTrendPoint `json:"powerTrend48h"`
}

// ===================== Power Storage =====================

// PowerStorageResponse 功率与储能响应
type PowerStorageResponse struct {
	SOC                 float64             `json:"soc"`
	SOH                 float64             `json:"soh"`
	DischargePowerLimit float64             `json:"dischargePowerLimit"`
	ChargePowerLimit    float64             `json:"chargePowerLimit"`
	AvailableCapacity   float64             `json:"availableCapacity"`
	BackupRequired      float64             `json:"backupRequired"`
	BatteryTemp         float64             `json:"batteryTemp"`
	Status              string              `json:"status"`
	CurrentMode         string              `json:"currentMode"`
	Trend48h            []StorageTrendPoint `json:"trend48h"`
}

// StorageTrendPoint 储能趋势数据点
type StorageTrendPoint struct {
	Timestamp     int64   `json:"timestamp"`
	ComputePower  float64 `json:"computePower"`
	StorageStatus float64 `json:"storageStatus"`
	PVOutput      float64 `json:"pvOutput"`
}

// ===================== Service Chain =====================

// ServiceNode 服务节点（适配前端 ServiceStatusTable 组件）
type ServiceNode struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Status      string  `json:"status"`
	SLORate     float64 `json:"sloRate"`
	Latency     float64 `json:"latency"`
	Throughput  float64 `json:"throughput"`
	ErrorRate   float64 `json:"errorRate"`
	CPUUsage    float64 `json:"cpuUsage"`
	MemoryUsage float64 `json:"memoryUsage"`
}

// ServiceChainResponse 服务链路响应
type ServiceChainResponse struct {
	Services []ServiceNode `json:"services"`
}
