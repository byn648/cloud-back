package greenrepo

import (
	"context"
	"database/sql"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	appcfg "github.com/yanshicheng/kube-nova/common/config"
)

type Metrics struct {
	ID             uint64
	NodeUuid       string
	ClusterUuid    string
	MetricTime     time.Time
	CPUUsage       float64
	GPUUsage       float64
	GPUMemoryUsage float64
	MemoryUsage    float64
	IORead         float64
	IOWrite        float64
	NetworkRx      float64
	NetworkTx      float64
	NodePower      float64
	UPSPower       float64
	ServiceEnergy  float64
	TaskType       string
	BatchSize      int
	Concurrency    int
	RequestRate    float64
	ReplicaCount   int
	SchedulePolicy string
}

type SLORecord struct {
	ID            uint64
	ServiceName   string
	WindowStart   time.Time
	WindowEnd     time.Time
	TotalRequests int64
	ErrorRequests int64
	P50Latency    float64
	P95Latency    float64
	P99Latency    float64
	SLORate       float64
}

type SLOMetricTS struct {
	ID               uint64
	ClusterUuid      string
	ServiceID        string
	ServiceName      string
	APIID            string
	MetricTime       time.Time
	QPS              float64
	P95Latency       float64
	P99Latency       float64
	ErrorRate        float64
	ReplicaCount     int
	CPUUtil          float64
	GPUUtil          float64
	NodePower        float64
	P99LatencyTarget float64
	ErrorRateTarget  float64
}

type SLOStatusRecord struct {
	ID            uint64
	ClusterUuid   string
	ServiceID     string
	ServiceName   string
	APIID         string
	StatusTime    time.Time
	SLOStatus     string
	ViolationRisk float64
	Reason        string
	QPS           float64
	P95Latency    float64
	P99Latency    float64
	ErrorRate     float64
	ReplicaCount  int
	CPUUtil       float64
	GPUUtil       float64
	NodePower     float64
}

type SLOObjective struct {
	ID               uint64
	ObjectiveID      string
	ClusterUuid      string
	BusinessFlow     string
	TargetP99Latency float64
	TargetErrorRate  float64
	AllocationMethod string
}

type SLOServiceDependency struct {
	ID                  uint64
	ObjectiveID         string
	UpstreamServiceID   string
	DownstreamServiceID string
	ServiceID           string
	ServiceName         string
	APIID               string
	IsCritical          bool
	PathOrder           int
}

type SLOServiceMetricAgg struct {
	ClusterUuid  string
	ServiceID    string
	ServiceName  string
	APIID        string
	AvgQPS       float64
	StdQPS       float64
	AvgP99       float64
	AvgErrorRate float64
	SampleCount  int64
}

type SLOBudgetAllocationRecord struct {
	ID                  uint64
	ObjectiveID         string
	ClusterUuid         string
	BusinessFlow        string
	ServiceID           string
	ServiceName         string
	APIID               string
	AllocationMethod    string
	LatencyContribution float64
	QPS                 float64
	QPSCV               float64
	ErrorRate           float64
	CriticalPath        bool
	RiskWeight          float64
	BudgetRatio         float64
	P99LatencyBudget    float64
	ErrorRateBudget     float64
	AllocatedAt         time.Time
}

type SLOProxyPredictionRecord struct {
	ID                   uint64
	ObjectiveID          string
	ClusterUuid          string
	BusinessFlow         string
	ServiceID            string
	ServiceName          string
	APIID                string
	ForecastTime         time.Time
	HorizonMinutes       int
	QPSForecast          float64
	RequestMix           string
	ReplicaCount         int
	CPURequest           float64
	GPURequest           float64
	MemoryRequestGB      float64
	PredictedCPUUtil     float64
	PredictedGPUUtil     float64
	P99LatencyBudget     float64
	ErrorRateBudget      float64
	PredictedP95Latency  float64
	PredictedP99Latency  float64
	ViolationProbability float64
	ModelName            string
	ModelVersion         string
	CreatedAt            time.Time
}

type ForecastRecord struct {
	ID                     uint64
	ForecastTime           time.Time
	NodeUuid               string
	ClusterUuid            string
	PredictedCPUUsage      float64
	PredictedGPUUsage      float64
	PredictedMemoryUsage   float64
	PredictedNodePower     float64
	PredictedServiceEnergy float64
	UpperBound             float64
	LowerBound             float64
	ConfidenceLevel        float64
	TaskType               string
	CreatedAt              time.Time
}

type ResourceSnapshot struct {
	ID               uint64
	ClusterUuid      string
	SnapshotTime     time.Time
	CPUContention    float64
	GPUContention    float64
	MemoryUsage      float64
	NetworkBandwidth float64
	ComputePower     float64
	StoragePower     float64
	SOC              float64
	SOH              float64
	BatteryTemp      float64
	PVOutput         float64
}

type Service struct {
	db    *sql.DB
	useDB bool
}

func NewService(mysqlCfg appcfg.MysqlConfig) *Service {
	s := &Service{}
	if !mysqlCfg.Enabled || strings.TrimSpace(mysqlCfg.DataSource) == "" {
		return s
	}
	db, err := sql.Open("mysql", mysqlCfg.DataSource)
	if err != nil {
		return s
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return s
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	s.db = db
	s.useDB = true
	return s
}

func (s *Service) Close() {
	if s.db != nil {
		_ = s.db.Close()
	}
}

// GetMetricsLastNHours returns metrics from the last N hours for a given node.
func (s *Service) GetMetricsLastNHours(nodeUuid string, hours int) ([]Metrics, error) {
	if !s.useDB {
		return nil, nil
	}
	query := `
SELECT id, node_uuid, cluster_uuid, metric_time,
       cpu_usage, gpu_usage, gpu_memory_usage, memory_usage,
       io_read, io_write, network_rx, network_tx,
       node_power, ups_power, service_energy,
       task_type, batch_size, concurrency, request_rate,
       replica_count, schedule_policy
FROM green_node_metrics
WHERE node_uuid = ?
  AND metric_time >= DATE_SUB(NOW(), INTERVAL ? HOUR)
ORDER BY metric_time ASC`
	rows, err := s.db.Query(query, nodeUuid, hours)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []Metrics
	for rows.Next() {
		var m Metrics
		err := rows.Scan(
			&m.ID, &m.NodeUuid, &m.ClusterUuid, &m.MetricTime,
			&m.CPUUsage, &m.GPUUsage, &m.GPUMemoryUsage, &m.MemoryUsage,
			&m.IORead, &m.IOWrite, &m.NetworkRx, &m.NetworkTx,
			&m.NodePower, &m.UPSPower, &m.ServiceEnergy,
			&m.TaskType, &m.BatchSize, &m.Concurrency, &m.RequestRate,
			&m.ReplicaCount, &m.SchedulePolicy,
		)
		if err != nil {
			return nil, err
		}
		result = append(result, m)
	}
	return result, rows.Err()
}

// GetMetricsByTaskType returns the most recent N metrics filtered by task type.
func (s *Service) GetMetricsByTaskType(taskType string, limit int) ([]Metrics, error) {
	if !s.useDB {
		return nil, nil
	}
	query := `
SELECT id, node_uuid, cluster_uuid, metric_time,
       cpu_usage, gpu_usage, gpu_memory_usage, memory_usage,
       io_read, io_write, network_rx, network_tx,
       node_power, ups_power, service_energy,
       task_type, batch_size, concurrency, request_rate,
       replica_count, schedule_policy
FROM green_node_metrics
WHERE task_type = ?
ORDER BY metric_time DESC
LIMIT ?`
	rows, err := s.db.Query(query, taskType, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []Metrics
	for rows.Next() {
		var m Metrics
		err := rows.Scan(
			&m.ID, &m.NodeUuid, &m.ClusterUuid, &m.MetricTime,
			&m.CPUUsage, &m.GPUUsage, &m.GPUMemoryUsage, &m.MemoryUsage,
			&m.IORead, &m.IOWrite, &m.NetworkRx, &m.NetworkTx,
			&m.NodePower, &m.UPSPower, &m.ServiceEnergy,
			&m.TaskType, &m.BatchSize, &m.Concurrency, &m.RequestRate,
			&m.ReplicaCount, &m.SchedulePolicy,
		)
		if err != nil {
			return nil, err
		}
		result = append(result, m)
	}
	return result, rows.Err()
}

// GetAllRecentMetrics returns the most recent N metrics across all nodes.
func (s *Service) GetAllRecentMetrics(limit int) ([]Metrics, error) {
	if !s.useDB {
		return nil, nil
	}
	query := `
SELECT id, node_uuid, cluster_uuid, metric_time,
       cpu_usage, gpu_usage, gpu_memory_usage, memory_usage,
       io_read, io_write, network_rx, network_tx,
       node_power, ups_power, service_energy,
       task_type, batch_size, concurrency, request_rate,
       replica_count, schedule_policy
FROM green_node_metrics
ORDER BY metric_time DESC
LIMIT ?`
	rows, err := s.db.Query(query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []Metrics
	for rows.Next() {
		var m Metrics
		err := rows.Scan(
			&m.ID, &m.NodeUuid, &m.ClusterUuid, &m.MetricTime,
			&m.CPUUsage, &m.GPUUsage, &m.GPUMemoryUsage, &m.MemoryUsage,
			&m.IORead, &m.IOWrite, &m.NetworkRx, &m.NetworkTx,
			&m.NodePower, &m.UPSPower, &m.ServiceEnergy,
			&m.TaskType, &m.BatchSize, &m.Concurrency, &m.RequestRate,
			&m.ReplicaCount, &m.SchedulePolicy,
		)
		if err != nil {
			return nil, err
		}
		result = append(result, m)
	}
	return result, rows.Err()
}

// GetSLORecordsLast48h returns SLO records for each service in the last 48 hours.
func (s *Service) GetSLORecordsLast48h() (map[string][]SLORecord, error) {
	if !s.useDB {
		return nil, nil
	}
	query := `
SELECT id, service_name, slo_window_start, slo_window_end,
       total_requests, error_requests,
       p50_latency, p95_latency, p99_latency, slo_rate
FROM green_slo_records
WHERE slo_window_end >= DATE_SUB(NOW(), INTERVAL 48 HOUR)
ORDER BY service_name, slo_window_start ASC`
	rows, err := s.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string][]SLORecord)
	for rows.Next() {
		var r SLORecord
		err := rows.Scan(
			&r.ID, &r.ServiceName, &r.WindowStart, &r.WindowEnd,
			&r.TotalRequests, &r.ErrorRequests,
			&r.P50Latency, &r.P95Latency, &r.P99Latency, &r.SLORate,
		)
		if err != nil {
			return nil, err
		}
		result[r.ServiceName] = append(result[r.ServiceName], r)
	}
	return result, rows.Err()
}

// GetAggregatedSLO aggregates SLO records per service over the last 48h.
func (s *Service) GetAggregatedSLO() (map[string]struct {
	AvgRate  float64
	AvgP50   float64
	AvgP95   float64
	TotalReq int64
	TotalErr int64
}, error) {
	if !s.useDB {
		return nil, nil
	}
	query := `
SELECT
  service_name,
  AVG(slo_rate) AS avg_rate,
  AVG(p50_latency) AS avg_p50,
  AVG(p95_latency) AS avg_p95,
  SUM(total_requests) AS total_req,
  SUM(error_requests) AS total_err
FROM green_slo_records
WHERE slo_window_end >= DATE_SUB(NOW(), INTERVAL 48 HOUR)
GROUP BY service_name`
	rows, err := s.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]struct {
		AvgRate  float64
		AvgP50   float64
		AvgP95   float64
		TotalReq int64
		TotalErr int64
	})
	for rows.Next() {
		var svc string
		var agg struct {
			AvgRate  float64
			AvgP50   float64
			AvgP95   float64
			TotalReq int64
			TotalErr int64
		}
		if err := rows.Scan(&svc, &agg.AvgRate, &agg.AvgP50, &agg.AvgP95, &agg.TotalReq, &agg.TotalErr); err != nil {
			return nil, err
		}
		result[svc] = agg
	}
	return result, rows.Err()
}

// GetLatestSLOStatuses returns the latest V0.1 status for each service/API pair.
func (s *Service) GetLatestSLOStatuses() ([]SLOStatusRecord, error) {
	if !s.useDB {
		return nil, nil
	}
	query := `
SELECT st.id, st.cluster_uuid, st.service_id, st.service_name, st.api_id, st.status_time,
       st.slo_status, st.violation_risk, st.reason,
       st.qps, st.p95_latency, st.p99_latency, st.error_rate, st.replica_count,
       st.cpu_util, st.gpu_util, st.node_power
FROM slo_status st
JOIN (
  SELECT cluster_uuid, service_id, api_id, MAX(status_time) AS latest_time
  FROM slo_status
  GROUP BY cluster_uuid, service_id, api_id
) latest
  ON latest.cluster_uuid = st.cluster_uuid
 AND latest.service_id = st.service_id
 AND latest.api_id = st.api_id
 AND latest.latest_time = st.status_time
ORDER BY st.violation_risk DESC, st.status_time DESC`
	rows, err := s.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []SLOStatusRecord
	for rows.Next() {
		var r SLOStatusRecord
		if err := rows.Scan(
			&r.ID, &r.ClusterUuid, &r.ServiceID, &r.ServiceName, &r.APIID, &r.StatusTime,
			&r.SLOStatus, &r.ViolationRisk, &r.Reason,
			&r.QPS, &r.P95Latency, &r.P99Latency, &r.ErrorRate, &r.ReplicaCount,
			&r.CPUUtil, &r.GPUUtil, &r.NodePower,
		); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

// GetSLOStatusesLast48h returns V0.1 status records for risk trend rendering.
func (s *Service) GetSLOStatusesLast48h() ([]SLOStatusRecord, error) {
	if !s.useDB {
		return nil, nil
	}
	query := `
SELECT id, cluster_uuid, service_id, service_name, api_id, status_time,
       slo_status, violation_risk, reason,
       qps, p95_latency, p99_latency, error_rate, replica_count,
       cpu_util, gpu_util, node_power
FROM slo_status
WHERE status_time >= DATE_SUB(NOW(), INTERVAL 48 HOUR)
ORDER BY status_time ASC, service_id ASC, api_id ASC`
	rows, err := s.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []SLOStatusRecord
	for rows.Next() {
		var r SLOStatusRecord
		if err := rows.Scan(
			&r.ID, &r.ClusterUuid, &r.ServiceID, &r.ServiceName, &r.APIID, &r.StatusTime,
			&r.SLOStatus, &r.ViolationRisk, &r.Reason,
			&r.QPS, &r.P95Latency, &r.P99Latency, &r.ErrorRate, &r.ReplicaCount,
			&r.CPUUtil, &r.GPUUtil, &r.NodePower,
		); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

// GetActiveSLOObjective returns the active end-to-end objective for V0.2 allocation.
func (s *Service) GetActiveSLOObjective(clusterUuid string) (*SLOObjective, error) {
	if !s.useDB {
		return nil, nil
	}
	query := `
SELECT id, objective_id, cluster_uuid, business_flow,
       target_p99_latency, target_error_rate, allocation_method
FROM slo_e2e_objective
WHERE is_active = 1
  AND (cluster_uuid = ? OR cluster_uuid = '')
ORDER BY cluster_uuid DESC, updated_at DESC
LIMIT 1`
	var obj SLOObjective
	err := s.db.QueryRow(query, clusterUuid).Scan(
		&obj.ID, &obj.ObjectiveID, &obj.ClusterUuid, &obj.BusinessFlow,
		&obj.TargetP99Latency, &obj.TargetErrorRate, &obj.AllocationMethod,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &obj, nil
}

// GetSLOServiceDependencies returns the service graph for an end-to-end objective.
func (s *Service) GetSLOServiceDependencies(objectiveID string) ([]SLOServiceDependency, error) {
	if !s.useDB {
		return nil, nil
	}
	query := `
SELECT id, objective_id, upstream_service_id, downstream_service_id,
       service_id, service_name, api_id, is_critical, path_order
FROM slo_service_dependency
WHERE objective_id = ?
ORDER BY path_order ASC, service_id ASC`
	rows, err := s.db.Query(query, objectiveID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []SLOServiceDependency
	for rows.Next() {
		var dep SLOServiceDependency
		var critical int
		if err := rows.Scan(
			&dep.ID, &dep.ObjectiveID, &dep.UpstreamServiceID, &dep.DownstreamServiceID,
			&dep.ServiceID, &dep.ServiceName, &dep.APIID, &critical, &dep.PathOrder,
		); err != nil {
			return nil, err
		}
		dep.IsCritical = critical != 0
		result = append(result, dep)
	}
	return result, rows.Err()
}

// GetSLOServiceMetricAggLast48h aggregates recent V0.1 samples for V0.2 allocation.
func (s *Service) GetSLOServiceMetricAggLast48h(clusterUuid string) ([]SLOServiceMetricAgg, error) {
	if !s.useDB {
		return nil, nil
	}
	query := `
SELECT cluster_uuid, service_id, service_name, api_id,
       AVG(qps) AS avg_qps,
       COALESCE(STDDEV_POP(qps), 0) AS std_qps,
       AVG(p99_latency) AS avg_p99,
       AVG(error_rate) AS avg_error_rate,
       COUNT(*) AS sample_count
FROM slo_metric_ts
WHERE metric_time >= DATE_SUB(NOW(), INTERVAL 48 HOUR)
  AND (? = '' OR cluster_uuid = ?)
GROUP BY cluster_uuid, service_id, service_name, api_id
ORDER BY service_id ASC, api_id ASC`
	rows, err := s.db.Query(query, clusterUuid, clusterUuid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []SLOServiceMetricAgg
	for rows.Next() {
		var agg SLOServiceMetricAgg
		if err := rows.Scan(
			&agg.ClusterUuid, &agg.ServiceID, &agg.ServiceName, &agg.APIID,
			&agg.AvgQPS, &agg.StdQPS, &agg.AvgP99, &agg.AvgErrorRate, &agg.SampleCount,
		); err != nil {
			return nil, err
		}
		result = append(result, agg)
	}
	return result, rows.Err()
}

// UpsertSLOBudgetAllocation stores the latest service-level budget allocation.
func (s *Service) UpsertSLOBudgetAllocation(r *SLOBudgetAllocationRecord) error {
	if !s.useDB {
		return nil
	}
	query := `
INSERT INTO slo_budget_allocation
  (objective_id, cluster_uuid, business_flow, service_id, service_name, api_id,
   allocation_method, latency_contribution, qps, qps_cv, error_rate,
   critical_path, risk_weight, budget_ratio, p99_latency_budget, error_rate_budget, allocated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
  business_flow = VALUES(business_flow),
  service_name = VALUES(service_name),
  latency_contribution = VALUES(latency_contribution),
  qps = VALUES(qps),
  qps_cv = VALUES(qps_cv),
  error_rate = VALUES(error_rate),
  critical_path = VALUES(critical_path),
  risk_weight = VALUES(risk_weight),
  budget_ratio = VALUES(budget_ratio),
  p99_latency_budget = VALUES(p99_latency_budget),
  error_rate_budget = VALUES(error_rate_budget),
  allocated_at = VALUES(allocated_at)`
	critical := 0
	if r.CriticalPath {
		critical = 1
	}
	_, err := s.db.Exec(query,
		r.ObjectiveID, r.ClusterUuid, r.BusinessFlow, r.ServiceID, r.ServiceName, r.APIID,
		r.AllocationMethod, r.LatencyContribution, r.QPS, r.QPSCV, r.ErrorRate,
		critical, r.RiskWeight, r.BudgetRatio, r.P99LatencyBudget, r.ErrorRateBudget, r.AllocatedAt,
	)
	return err
}

// GetLatestSLOProxyPredictions returns V0.3 proxy-model predictions for UI rendering.
func (s *Service) GetLatestSLOProxyPredictions() ([]SLOProxyPredictionRecord, error) {
	if !s.useDB {
		return nil, nil
	}
	query := `
SELECT id, objective_id, cluster_uuid, business_flow, service_id, service_name, api_id,
       forecast_time, horizon_minutes, qps_forecast, request_mix, replica_count,
       cpu_request, gpu_request, memory_request_gb, predicted_cpu_util, predicted_gpu_util,
       p99_latency_budget, error_rate_budget, predicted_p95_latency, predicted_p99_latency,
       violation_probability, model_name, model_version, created_at
FROM slo_proxy_prediction
ORDER BY objective_id ASC, service_id ASC, api_id ASC, horizon_minutes ASC, created_at DESC`
	rows, err := s.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]SLOProxyPredictionRecord, 0)
	for rows.Next() {
		var r SLOProxyPredictionRecord
		if err := rows.Scan(
			&r.ID, &r.ObjectiveID, &r.ClusterUuid, &r.BusinessFlow, &r.ServiceID, &r.ServiceName, &r.APIID,
			&r.ForecastTime, &r.HorizonMinutes, &r.QPSForecast, &r.RequestMix, &r.ReplicaCount,
			&r.CPURequest, &r.GPURequest, &r.MemoryRequestGB, &r.PredictedCPUUtil, &r.PredictedGPUUtil,
			&r.P99LatencyBudget, &r.ErrorRateBudget, &r.PredictedP95Latency, &r.PredictedP99Latency,
			&r.ViolationProbability, &r.ModelName, &r.ModelVersion, &r.CreatedAt,
		); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

// UpsertSLOProxyPrediction stores a V0.3 resource-to-SLO prediction.
func (s *Service) UpsertSLOProxyPrediction(r *SLOProxyPredictionRecord) error {
	if !s.useDB {
		return nil
	}
	query := `
INSERT INTO slo_proxy_prediction
  (objective_id, cluster_uuid, business_flow, service_id, service_name, api_id,
   forecast_time, horizon_minutes, qps_forecast, request_mix, replica_count,
   cpu_request, gpu_request, memory_request_gb, predicted_cpu_util, predicted_gpu_util,
   p99_latency_budget, error_rate_budget, predicted_p95_latency, predicted_p99_latency,
   violation_probability, model_name, model_version)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
  cluster_uuid = VALUES(cluster_uuid),
  business_flow = VALUES(business_flow),
  service_name = VALUES(service_name),
  forecast_time = VALUES(forecast_time),
  qps_forecast = VALUES(qps_forecast),
  request_mix = VALUES(request_mix),
  replica_count = VALUES(replica_count),
  cpu_request = VALUES(cpu_request),
  gpu_request = VALUES(gpu_request),
  memory_request_gb = VALUES(memory_request_gb),
  predicted_cpu_util = VALUES(predicted_cpu_util),
  predicted_gpu_util = VALUES(predicted_gpu_util),
  p99_latency_budget = VALUES(p99_latency_budget),
  error_rate_budget = VALUES(error_rate_budget),
  predicted_p95_latency = VALUES(predicted_p95_latency),
  predicted_p99_latency = VALUES(predicted_p99_latency),
  violation_probability = VALUES(violation_probability),
  model_version = VALUES(model_version),
  created_at = CURRENT_TIMESTAMP`
	_, err := s.db.Exec(query,
		r.ObjectiveID, r.ClusterUuid, r.BusinessFlow, r.ServiceID, r.ServiceName, r.APIID,
		r.ForecastTime, r.HorizonMinutes, r.QPSForecast, r.RequestMix, r.ReplicaCount,
		r.CPURequest, r.GPURequest, r.MemoryRequestGB, r.PredictedCPUUtil, r.PredictedGPUUtil,
		r.P99LatencyBudget, r.ErrorRateBudget, r.PredictedP95Latency, r.PredictedP99Latency,
		r.ViolationProbability, r.ModelName, r.ModelVersion,
	)
	return err
}

// GetResourceSnapshotsLast48h returns resource snapshots for the last 48 hours.
func (s *Service) GetResourceSnapshotsLast48h(clusterUuid string) ([]ResourceSnapshot, error) {
	if !s.useDB {
		return nil, nil
	}
	query := `
SELECT id, cluster_uuid, snapshot_time,
       cpu_contention, gpu_contention, memory_usage, network_bandwidth,
       compute_power, storage_power, soc, soh, battery_temp, pv_output
FROM green_resource_snapshot
WHERE cluster_uuid = ?
  AND snapshot_time >= DATE_SUB(NOW(), INTERVAL 48 HOUR)
ORDER BY snapshot_time ASC`
	rows, err := s.db.Query(query, clusterUuid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []ResourceSnapshot
	for rows.Next() {
		var r ResourceSnapshot
		err := rows.Scan(
			&r.ID, &r.ClusterUuid, &r.SnapshotTime,
			&r.CPUContention, &r.GPUContention, &r.MemoryUsage, &r.NetworkBandwidth,
			&r.ComputePower, &r.StoragePower, &r.SOC, &r.SOH, &r.BatteryTemp, &r.PVOutput,
		)
		if err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

// GetLatestResourceSnapshot returns the most recent resource snapshot.
func (s *Service) GetLatestResourceSnapshot(clusterUuid string) (*ResourceSnapshot, error) {
	if !s.useDB {
		return nil, nil
	}
	query := `
SELECT id, cluster_uuid, snapshot_time,
       cpu_contention, gpu_contention, memory_usage, network_bandwidth,
       compute_power, storage_power, soc, soh, battery_temp, pv_output
FROM green_resource_snapshot
WHERE cluster_uuid = ?
ORDER BY snapshot_time DESC
LIMIT 1`
	var r ResourceSnapshot
	err := s.db.QueryRow(query, clusterUuid).Scan(
		&r.ID, &r.ClusterUuid, &r.SnapshotTime,
		&r.CPUContention, &r.GPUContention, &r.MemoryUsage, &r.NetworkBandwidth,
		&r.ComputePower, &r.StoragePower, &r.SOC, &r.SOH, &r.BatteryTemp, &r.PVOutput,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// InsertMetric inserts a new metrics record.
func (s *Service) InsertMetric(m *Metrics) error {
	if !s.useDB {
		return nil
	}
	query := `
INSERT INTO green_node_metrics
  (node_uuid, cluster_uuid, metric_time, cpu_usage, gpu_usage, gpu_memory_usage, memory_usage,
   io_read, io_write, network_rx, network_tx, node_power, ups_power, service_energy,
   task_type, batch_size, concurrency, request_rate, replica_count, schedule_policy)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE cpu_usage = VALUES(cpu_usage)`
	_, err := s.db.Exec(query,
		m.NodeUuid, m.ClusterUuid, m.MetricTime,
		m.CPUUsage, m.GPUUsage, m.GPUMemoryUsage, m.MemoryUsage,
		m.IORead, m.IOWrite, m.NetworkRx, m.NetworkTx,
		m.NodePower, m.UPSPower, m.ServiceEnergy,
		m.TaskType, m.BatchSize, m.Concurrency, m.RequestRate,
		m.ReplicaCount, m.SchedulePolicy,
	)
	return err
}

// InsertSLOMetric inserts a service/API level SLO metric sample.
func (s *Service) InsertSLOMetric(m *SLOMetricTS) error {
	if !s.useDB {
		return nil
	}
	query := `
INSERT INTO slo_metric_ts
  (cluster_uuid, service_id, service_name, api_id, metric_time,
   qps, p95_latency, p99_latency, error_rate, replica_count,
   cpu_util, gpu_util, node_power, p99_latency_target, error_rate_target)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
  qps = VALUES(qps),
  p95_latency = VALUES(p95_latency),
  p99_latency = VALUES(p99_latency),
  error_rate = VALUES(error_rate),
  replica_count = VALUES(replica_count),
  cpu_util = VALUES(cpu_util),
  gpu_util = VALUES(gpu_util),
  node_power = VALUES(node_power)`
	_, err := s.db.Exec(query,
		m.ClusterUuid, m.ServiceID, m.ServiceName, m.APIID, m.MetricTime,
		m.QPS, m.P95Latency, m.P99Latency, m.ErrorRate, m.ReplicaCount,
		m.CPUUtil, m.GPUUtil, m.NodePower, m.P99LatencyTarget, m.ErrorRateTarget,
	)
	return err
}

// UpsertSLOStatus stores the V0.1 rule/risk judgment for a SLO metric sample.
func (s *Service) UpsertSLOStatus(r *SLOStatusRecord) error {
	if !s.useDB {
		return nil
	}
	query := `
INSERT INTO slo_status
  (cluster_uuid, service_id, service_name, api_id, status_time,
   slo_status, violation_risk, reason,
   qps, p95_latency, p99_latency, error_rate, replica_count,
   cpu_util, gpu_util, node_power)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
  slo_status = VALUES(slo_status),
  violation_risk = VALUES(violation_risk),
  reason = VALUES(reason),
  qps = VALUES(qps),
  p95_latency = VALUES(p95_latency),
  p99_latency = VALUES(p99_latency),
  error_rate = VALUES(error_rate),
  replica_count = VALUES(replica_count),
  cpu_util = VALUES(cpu_util),
  gpu_util = VALUES(gpu_util),
  node_power = VALUES(node_power)`
	_, err := s.db.Exec(query,
		r.ClusterUuid, r.ServiceID, r.ServiceName, r.APIID, r.StatusTime,
		r.SLOStatus, r.ViolationRisk, r.Reason,
		r.QPS, r.P95Latency, r.P99Latency, r.ErrorRate, r.ReplicaCount,
		r.CPUUtil, r.GPUUtil, r.NodePower,
	)
	return err
}

// InsertSLORecord inserts a compatibility SLO window record for existing charts.
func (s *Service) InsertSLORecord(r *SLORecord) error {
	if !s.useDB {
		return nil
	}
	query := `
INSERT INTO green_slo_records
  (service_name, slo_window_start, slo_window_end, total_requests, error_requests,
   p50_latency, p95_latency, p99_latency, slo_rate)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`
	_, err := s.db.Exec(query,
		r.ServiceName, r.WindowStart, r.WindowEnd, r.TotalRequests, r.ErrorRequests,
		r.P50Latency, r.P95Latency, r.P99Latency, r.SLORate,
	)
	return err
}

// GetLatestMetric returns the most recent metric record for a given node.
func (s *Service) GetLatestMetric(nodeUuid string) (*Metrics, error) {
	if !s.useDB {
		return nil, nil
	}
	query := `
SELECT id, node_uuid, cluster_uuid, metric_time,
       cpu_usage, gpu_usage, gpu_memory_usage, memory_usage,
       io_read, io_write, network_rx, network_tx,
       node_power, ups_power, service_energy,
       task_type, batch_size, concurrency, request_rate,
       replica_count, schedule_policy
FROM green_node_metrics
WHERE node_uuid = ?
ORDER BY metric_time DESC
LIMIT 1`
	var m Metrics
	err := s.db.QueryRow(query, nodeUuid).Scan(
		&m.ID, &m.NodeUuid, &m.ClusterUuid, &m.MetricTime,
		&m.CPUUsage, &m.GPUUsage, &m.GPUMemoryUsage, &m.MemoryUsage,
		&m.IORead, &m.IOWrite, &m.NetworkRx, &m.NetworkTx,
		&m.NodePower, &m.UPSPower, &m.ServiceEnergy,
		&m.TaskType, &m.BatchSize, &m.Concurrency, &m.RequestRate,
		&m.ReplicaCount, &m.SchedulePolicy,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// InsertForecast inserts a forecast result record.
func (s *Service) InsertForecast(f *ForecastRecord) error {
	if !s.useDB {
		return nil
	}
	query := `
INSERT INTO green_energy_forecast
  (forecast_time, node_uuid, cluster_uuid, predicted_cpu_usage, predicted_gpu_usage,
   predicted_memory_usage, predicted_node_power, predicted_service_energy,
   upper_bound, lower_bound, confidence_level, task_type)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE predicted_cpu_usage = VALUES(predicted_cpu_usage)`
	_, err := s.db.Exec(query,
		f.ForecastTime, f.NodeUuid, f.ClusterUuid,
		f.PredictedCPUUsage, f.PredictedGPUUsage, f.PredictedMemoryUsage,
		f.PredictedNodePower, f.PredictedServiceEnergy,
		f.UpperBound, f.LowerBound, f.ConfidenceLevel, f.TaskType,
	)
	return err
}

// GetForecastsLastNHours returns forecast records from the last N hours for a given node.
func (s *Service) GetForecastsLastNHours(nodeUuid string, hours int) ([]ForecastRecord, error) {
	if !s.useDB {
		return nil, nil
	}
	query := `
	SELECT id, forecast_time, node_uuid, cluster_uuid,
	       predicted_cpu_usage, predicted_gpu_usage, predicted_memory_usage,
	       predicted_node_power, predicted_service_energy,
	       upper_bound, lower_bound, confidence_level, task_type, created_at
	FROM green_energy_forecast
	WHERE node_uuid = ?
	  AND forecast_time >= DATE_SUB(NOW(), INTERVAL ? HOUR)
	ORDER BY forecast_time ASC`
	rows, err := s.db.Query(query, nodeUuid, hours)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []ForecastRecord
	for rows.Next() {
		var f ForecastRecord
		err := rows.Scan(
			&f.ID, &f.ForecastTime, &f.NodeUuid, &f.ClusterUuid,
			&f.PredictedCPUUsage, &f.PredictedGPUUsage, &f.PredictedMemoryUsage,
			&f.PredictedNodePower, &f.PredictedServiceEnergy,
			&f.UpperBound, &f.LowerBound, &f.ConfidenceLevel, &f.TaskType, &f.CreatedAt,
		)
		if err != nil {
			return nil, err
		}
		result = append(result, f)
	}
	return result, rows.Err()
}

// Query executes a raw query and returns *sql.Rows.
func (s *Service) Query(query string, args ...interface{}) (*sql.Rows, error) {
	if !s.useDB {
		return nil, nil
	}
	return s.db.Query(query, args...)
}

// Exec executes a raw statement and returns sql.Result.
func (s *Service) Exec(query string, args ...interface{}) (sql.Result, error) {
	if !s.useDB {
		return nil, nil
	}
	return s.db.Exec(query, args...)
}
