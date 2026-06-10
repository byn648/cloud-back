package types

import "time"

// ForecastRequest 预测分析查询请求
type ForecastRequest struct {
	TaskType        string  `form:"taskType"`
	Server          string  `form:"server"`
	Service         string  `form:"service"`
	Instance        string  `form:"instance"`
	TimeRange       string  `form:"timeRange"`       // 1d/7d/30d/90d
	ConfidenceLevel float64 `form:"confidenceLevel"` // 0.8/0.9/0.95/0.99
	ForecastHorizon int     `form:"forecastHorizon"` // 预测步长(hour)
	SmoothingFactor float64 `form:"smoothingFactor"` // 0.05~0.9
	DataSource      string  `form:"dataSource"`      // realtime/history
	RefreshInterval int     `form:"refreshInterval"` // 秒
}

// EnergyForecastRequest 能耗预测查询请求
type EnergyForecastRequest struct {
	Hours           int     `form:"hours"`           // 查询多少小时的历史数据，默认 24
	ConfidenceLevel float64 `form:"confidenceLevel"` // 置信度，默认 0.95
	TaskType        string  `form:"taskType"`        // ai_infer / ai_train / non_ai / all
}

// EnergyDataPoint 能耗预测数据点
type EnergyDataPoint struct {
	Timestamp   int64   `json:"timestamp"`
	CPUUsage    float64 `json:"cpuUsage"`
	GPUUsage    float64 `json:"gpuUsage"`
	MemoryUsage float64 `json:"memoryUsage"`
	NodePower   float64 `json:"nodePower"`
	Upper       float64 `json:"upper"`
	Lower       float64 `json:"lower"`
	IsForecast  bool    `json:"isForecast"`
}

// EnergyForecastResponse 能耗预测响应
type EnergyForecastResponse struct {
	History          []EnergyDataPoint `json:"history"`
	Forecast         []EnergyDataPoint `json:"forecast"`
	TotalEnergy      float64           `json:"totalEnergy"`    // 总预测能耗 kWh
	CarbonEmission   float64           `json:"carbonEmission"` // 预估碳排放 kg
	AvgCPU           float64           `json:"avgCpu"`
	AvgGPU           float64           `json:"avgGpu"`
	AvgPower         float64           `json:"avgPower"`
	CollectionStatus string            `json:"collectionStatus"` // collecting / idle / no_data
	LastCollectedAt  time.Time         `json:"lastCollectedAt"`
	ConfidenceLevel  float64           `json:"confidenceLevel"`
}

// CollectResponse 采集触发响应
type CollectResponse struct {
	Success   bool      `json:"success"`
	Message   string    `json:"message"`
	Timestamp time.Time `json:"timestamp"`
}

// HistoryDataPoint 历史数据点
type HistoryDataPoint struct {
	Timestamp        int64            `json:"timestamp"`
	CPUUsage         float64          `json:"cpuUsage"`
	GPUUsage         float64          `json:"gpuUsage"`
	GPUMemoryUsage   float64          `json:"gpuMemoryUsage"`
	MemoryUsage      float64          `json:"memoryUsage"`
	IORead           float64          `json:"ioRead"`
	IOWrite          float64          `json:"ioWrite"`
	NetworkRx        float64          `json:"networkRx"`
	NetworkTx        float64          `json:"networkTx"`
	NodePower        float64          `json:"nodePower"`
	UPSPower         float64          `json:"upsPower"`
	ServiceEnergy    float64          `json:"serviceEnergy"`
	TaskType         string           `json:"taskType"`
	BatchSize        int              `json:"batchSize"`
	Concurrency      int              `json:"concurrency"`
	RequestRate      float64          `json:"requestRate"`
	ResourceRequests ResourceRequests `json:"resourceRequests"`
	ReplicaCount     int              `json:"replicaCount"`
	SchedulePolicy   string           `json:"schedulePolicy"`
}

// ResourceRequests 资源请求配置
type ResourceRequests struct {
	CPU    string `json:"cpu"`
	Memory string `json:"memory"`
}

// ForecastDataPoint 预测数据点
type ForecastDataPoint struct {
	Timestamp      int64   `json:"timestamp"`
	CPUUsage       float64 `json:"cpuUsage"`
	GPUUsage       float64 `json:"gpuUsage"`
	GPUMemoryUsage float64 `json:"gpuMemoryUsage"`
	MemoryUsage    float64 `json:"memoryUsage"`
	NodePower      float64 `json:"nodePower"`
	Upper          float64 `json:"upper"`
	Lower          float64 `json:"lower"`
}

// InfluenceFactor 影响因素
type InfluenceFactor struct {
	Name  string  `json:"name"`
	Value float64 `json:"value"`
	Trend float64 `json:"trend"`
}

// ForecastResponse 预测分析响应
type ForecastResponse struct {
	History          []HistoryDataPoint  `json:"history"`
	Forecast         []ForecastDataPoint `json:"forecast"`
	InfluenceFactors []InfluenceFactor   `json:"influenceFactors"`
}
