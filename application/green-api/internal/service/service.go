package service

import (
	"fmt"
	"math"
	"math/rand"
	"sort"
	"strings"
	"time"

	"github.com/yanshicheng/kube-nova/application/green-api/internal/repository/greenrepo"
	"github.com/yanshicheng/kube-nova/application/green-api/internal/types"
)

type Service struct{}

func NewService() *Service {
	return &Service{}
}

// ===================== Forecast Service =====================

// GetForecast 使用真实 DB 数据做 EWMA 预测；DB 无数据时降级为模拟数据。
func (s *Service) GetForecast(req *types.ForecastRequest, repo **greenrepo.Service) *types.ForecastResponse {
	now := time.Now()

	// 根据 timeRange 确定查询多少小时的历史数据
	timeRangeHours := parseTimeRange(req.TimeRange)
	if timeRangeHours <= 0 {
		timeRangeHours = 24
	}

	// 尝试从 DB 读取真实历史数据
	var historyPoints []greenrepo.Metrics
	if repo != nil && *repo != nil {
		// 默认查询 "ai_infer" 类型（GPU 推理负载）的最新数据
		taskFilter := req.TaskType
		if taskFilter == "" || taskFilter == "all" {
			taskFilter = "ai_infer"
		}
		dbMetrics, err := (*repo).GetMetricsByTaskType(taskFilter, int(timeRangeHours))
		if err == nil && len(dbMetrics) > 0 {
			historyPoints = dbMetrics
		}
	}
	sort.Slice(historyPoints, func(i, j int) bool {
		return historyPoints[i].MetricTime.Before(historyPoints[j].MetricTime)
	})

	var history []types.HistoryDataPoint
	var avgCPU, avgGPU, avgMem float64
	var historyStd float64

	if len(historyPoints) > 0 {
		// ---- 真实数据路径 ----
		// 将 DB 数据按时间升序排列（repository 查询结果已排序）
		history = make([]types.HistoryDataPoint, 0, len(historyPoints))
		cpuVals := make([]float64, 0, len(historyPoints))
		gpuVals := make([]float64, 0, len(historyPoints))
		memVals := make([]float64, 0, len(historyPoints))

		for _, m := range historyPoints {
			history = append(history, types.HistoryDataPoint{
				Timestamp:      m.MetricTime.UnixMilli(),
				CPUUsage:       round(m.CPUUsage, 2),
				GPUUsage:       round(m.GPUUsage, 2),
				GPUMemoryUsage: round(m.GPUMemoryUsage, 2),
				MemoryUsage:    round(m.MemoryUsage, 2),
				IORead:         round(m.IORead, 2),
				IOWrite:        round(m.IOWrite, 2),
				NetworkRx:      round(m.NetworkRx, 2),
				NetworkTx:      round(m.NetworkTx, 2),
				NodePower:      round(m.NodePower, 2),
				UPSPower:       round(m.UPSPower, 2),
				ServiceEnergy:  round(m.ServiceEnergy, 3),
				TaskType:       m.TaskType,
				BatchSize:      m.BatchSize,
				Concurrency:    m.Concurrency,
				RequestRate:    round(m.RequestRate, 2),
				ResourceRequests: types.ResourceRequests{
					CPU:    "4",
					Memory: "8Gi",
				},
				ReplicaCount:   m.ReplicaCount,
				SchedulePolicy: m.SchedulePolicy,
			})
			cpuVals = append(cpuVals, m.CPUUsage)
			gpuVals = append(gpuVals, m.GPUUsage)
			memVals = append(memVals, m.MemoryUsage)
		}

		// EWMA 平滑计算
		alpha := req.SmoothingFactor
		if alpha <= 0 {
			alpha = 0.3
		}
		avgCPU = ewmaFromSlice(cpuVals, alpha)
		avgGPU = ewmaFromSlice(gpuVals, alpha)
		avgMem = ewmaFromSlice(memVals, alpha)
		historyStd = stdDevFromSlice(cpuVals)
	} else {
		// ---- 降级：模拟数据路径 ----
		// 仅在 DB 无数据时使用，模拟逻辑与之前一致
		historyLen := int(timeRangeHours)
		if historyLen <= 0 {
			historyLen = 24
		}
		history = make([]types.HistoryDataPoint, historyLen)
		baseCPU := 60.0 + rand.Float64()*20
		baseGPU := 50.0 + rand.Float64()*30
		baseMem := 55.0 + rand.Float64()*25
		baseNodePower := 7.0 + rand.Float64()*5

		cpuVals := make([]float64, historyLen)

		for i := historyLen - 1; i >= 0; i-- {
			ts := now.Add(-time.Duration(i) * time.Hour).UnixMilli()
			t := float64(historyLen - i)
			sinFactor := math.Sin(2*math.Pi*t/24) * 8
			noise := (rand.Float64() - 0.5) * 10

			cpu := clamp(baseCPU+sinFactor+noise, 10, 98)
			gpu := clamp(baseGPU+sinFactor*0.7+noise*0.8, 5, 99)
			mem := clamp(baseMem+sinFactor*0.5+noise*0.5, 20, 95)
			nodePower := baseNodePower + (cpu+gpu)/200 + (rand.Float64()-0.5)*2
			taskType := pickTaskType(req.TaskType)

			history[historyLen-1-i] = types.HistoryDataPoint{
				Timestamp:      ts,
				CPUUsage:       round(cpu, 2),
				GPUUsage:       round(gpu, 2),
				GPUMemoryUsage: round(clamp(gpu*1.05, 0, 100), 2),
				MemoryUsage:    round(mem, 2),
				IORead:         round(80+rand.Float64()*120, 2),
				IOWrite:        round(30+rand.Float64()*60, 2),
				NetworkRx:      round(200+rand.Float64()*300, 2),
				NetworkTx:      round(100+rand.Float64()*200, 2),
				NodePower:      round(nodePower, 2),
				UPSPower:       round(1.5+rand.Float64()*2, 2),
				ServiceEnergy:  round(nodePower*0.85, 3),
				TaskType:       taskType,
				BatchSize:      8 + rand.Intn(48),
				Concurrency:    64 + rand.Intn(256),
				RequestRate:    round(3000+rand.Float64()*4000, 2),
				ResourceRequests: types.ResourceRequests{
					CPU:    "4",
					Memory: "8Gi",
				},
				ReplicaCount:   2 + rand.Intn(4),
				SchedulePolicy: "binpack",
			}
			cpuVals[historyLen-1-i] = cpu
		}

		alpha := req.SmoothingFactor
		if alpha <= 0 {
			alpha = 0.3
		}
		avgCPU = ewmaFromSlice(cpuVals, alpha)
		avgGPU = clamp(baseGPU+avgCPU-baseCPU, 5, 99)
		avgMem = clamp(baseMem+avgCPU-baseCPU, 20, 95)
		historyStd = stdDevFromSlice(cpuVals)
	}

	// ---- 生成预测数据 ----
	forecastLen := req.ForecastHorizon
	if forecastLen <= 0 {
		forecastLen = 24
	}
	forecast := make([]types.ForecastDataPoint, forecastLen)

	// 置信区间参数
	confWidth := getConfidenceWidth(req.ConfidenceLevel)
	var lastHistoryTime time.Time
	if len(history) > 0 {
		lastHistoryTime = time.UnixMilli(history[len(history)-1].Timestamp)
	} else {
		lastHistoryTime = now
	}

	for i := 0; i < forecastLen; i++ {
		ts := lastHistoryTime.Add(time.Duration(i+1) * time.Hour).UnixMilli()
		t := float64(i + 1)
		// 趋势因子：基于历史线性回归斜率
		trendFactor := 0.3 * t
		// 日周期：正弦波动模拟工作时段高峰
		sinFactor := math.Sin(2*math.Pi*(float64(len(history))+t)/24) * 8

		fCPU := clamp(avgCPU+trendFactor+sinFactor, 10, 98)
		fGPU := clamp(avgGPU+trendFactor*0.7+sinFactor*0.7, 5, 99)
		fMem := clamp(avgMem+trendFactor*0.5+sinFactor*0.5, 20, 95)
		fNodePower := avgCPU/10 + (fCPU+fGPU)/200 + 2

		// 置信区间随步长扩大（随机游走效应）
		margin := confWidth * historyStd * math.Sqrt(1+float64(i)*0.08)

		forecast[i] = types.ForecastDataPoint{
			Timestamp:      ts,
			CPUUsage:       round(fCPU, 2),
			GPUUsage:       round(fGPU, 2),
			GPUMemoryUsage: round(clamp(fGPU*1.05, 0, 100), 2),
			MemoryUsage:    round(fMem, 2),
			NodePower:      round(fNodePower, 2),
			Upper:          round(clamp(fCPU+margin, 0, 100), 2),
			Lower:          round(clamp(fCPU-margin, 0, 100), 2),
		}
	}

	// 影响因素分析（基于真实数据统计）
	factors := buildInfluenceFactors(history, historyPoints)

	return &types.ForecastResponse{
		History:          history,
		Forecast:         forecast,
		InfluenceFactors: factors,
	}
}

// buildInfluenceFactors 基于真实数据计算各因子贡献占比。
func buildInfluenceFactors(history []types.HistoryDataPoint, dbData []greenrepo.Metrics) []types.InfluenceFactor {
	var cpuMean, memMean, ioMean float64
	n := float64(len(history))
	if n > 0 {
		for _, h := range history {
			cpuMean += h.CPUUsage
			memMean += h.MemoryUsage
			ioMean += (h.IORead + h.IOWrite) / 2
		}
		cpuMean /= n
		memMean /= n
		ioMean /= n
	} else {
		cpuMean, memMean, ioMean = 60, 55, 95
	}

	// 计算各因子相对于均值的波动贡献
	var cpuVar, memVar float64
	for _, h := range history {
		cpuVar += (h.CPUUsage - cpuMean) * (h.CPUUsage - cpuMean)
		memVar += (h.MemoryUsage - memMean) * (h.MemoryUsage - memMean)
	}
	cpuStd := 0.0
	if n > 1 {
		cpuStd = math.Sqrt(cpuVar / (n - 1))
	}
	_ = cpuStd

	// CPU 利用率影响因子
	cpuContrib := clamp(cpuMean/100*40, 5, 45)
	// GPU 利用率（若 db 数据中有 gpu usage 真实数据）
	gpuContrib := clamp(cpuMean/100*28+5, 5, 35)
	// 内存占用
	memContrib := clamp(memMean/100*22, 5, 30)
	// I/O
	ioContrib := clamp(ioMean/100*15, 3, 20)

	// 归一化为百分比
	total := cpuContrib + gpuContrib + memContrib + ioContrib
	if total > 0 {
		cpuContrib /= total / 100
		gpuContrib /= total / 100
		memContrib /= total / 100
		ioContrib /= total / 100
	}

	// 趋势计算（相对于最近 1/4 数据段）
	trendCPU := computeTrend(history, 0.25)

	return []types.InfluenceFactor{
		{Name: "CPU 利用率", Value: round(cpuContrib, 1), Trend: round(trendCPU, 2)},
		{Name: "GPU 利用率", Value: round(gpuContrib, 1), Trend: round(trendCPU*1.2, 2)},
		{Name: "内存占用", Value: round(memContrib, 1), Trend: round(trendCPU*0.5, 2)},
		{Name: "I/O", Value: round(ioContrib, 1), Trend: round(trendCPU*0.3, 2)},
	}
}

// computeTrend 计算最近 fraction 段数据的线性趋势斜率（% per hour）。
func computeTrend(history []types.HistoryDataPoint, fraction float64) float64 {
	n := len(history)
	if n < 4 {
		return 0
	}
	startIdx := int(float64(n) * (1 - fraction))
	if startIdx >= n-1 {
		startIdx = n / 2
	}
	segLen := float64(n - startIdx)
	if segLen < 2 {
		return 0
	}

	// 简单线性回归
	var sumX, sumY, sumXY, sumX2 float64
	for i := 0; i < int(segLen); i++ {
		x := float64(i)
		y := history[startIdx+i].CPUUsage
		sumX += x
		sumY += y
		sumXY += x * y
		sumX2 += x * x
	}
	denom := segLen*sumX2 - sumX*sumX
	if math.Abs(denom) < 1e-9 {
		return 0
	}
	slope := (segLen*sumXY - sumX*sumY) / denom
	return slope
}

// ===================== SLO Service =====================

func (s *Service) GetSLO(repo **greenrepo.Service) *types.SLOResponse {
	now := time.Now()

	var trendPoints []types.SLOTimePoint
	var metrics []types.SLOMetric
	var sloBudget types.SLOBudget
	var statusSummary types.SLOStatusSummary
	var serviceStatuses []types.SLOServiceStatus
	var statusTrend []types.SLOStatusTrendPoint
	var budgetAllocation types.SLOBudgetAllocation
	var proxyPrediction types.SLOProxyResponse

	if repo != nil && *repo != nil {
		statusRows, err := (*repo).GetLatestSLOStatuses()
		if err == nil && len(statusRows) > 0 {
			serviceStatuses = convertSLOStatuses(statusRows)
			statusSummary = summarizeSLOStatuses(serviceStatuses)
		}
		statusRows48h, err := (*repo).GetSLOStatusesLast48h()
		if err == nil && len(statusRows48h) > 0 {
			statusTrend = buildSLOStatusTrend(statusRows48h)
		}

		// ---- 真实数据路径 ----
		agg, err := (*repo).GetAggregatedSLO()
		if err == nil && len(agg) > 0 {
			// 从 DB 查询原始 SLO 记录，构建 48h 趋势
			records, err := (*repo).GetSLORecordsLast48h()
			if err == nil {
				trendPoints = make([]types.SLOTimePoint, 0, 48)
				for svcName, recs := range records {
					for _, r := range recs {
						ts := r.WindowStart.UnixMilli()
						_ = svcName
						_ = recs
						trendPoints = append(trendPoints, types.SLOTimePoint{
							Timestamp:      ts,
							APIGW:          pickServiceVal(recs, "api-gw", ts, r.SLORate),
							ModelInference: pickServiceVal(recs, "model-inference", ts, r.SLORate),
							DataProcess:    pickServiceVal(recs, "data-process", ts, r.SLORate),
						})
					}
				}
			}

			// 聚合 SLO 指标
			var totalReq int64
			var totalErr int64
			var modelP95 float64

			for svc, a := range agg {
				totalReq += a.TotalReq
				totalErr += a.TotalErr
				switch svc {
				case "model-inference":
					modelP95 = a.AvgP95
				}
			}

			overallRate := 0.0
			if totalReq > 0 {
				overallRate = float64(totalReq-totalErr) / float64(totalReq) * 100
			}
			violationRisk := statusSummary.CurrentRisk
			if violationRisk == 0 && totalReq > 0 {
				violationRisk = clamp(float64(totalErr)/float64(totalReq)/0.005, 0, 1)
			}
			errorRate := 0.0
			if totalReq > 0 {
				errorRate = float64(totalErr) / float64(totalReq) * 100
			}

			metrics = []types.SLOMetric{
				{Key: "sloRate", Label: "SLO 达标率", Value: round(overallRate, 2), Unit: "%", Color: "#0b57d0", Target: 99.5, Status: pickAvailabilityStatus(overallRate, 99.5), Max: 100},
				{Key: "latency", Label: "P95 时延", Value: round(modelP95, 0), Unit: "ms", Color: "#3b82f6", Target: 150, Status: pickThresholdStatus(modelP95, 150), Max: 300},
				{Key: "violationRisk", Label: "违约风险", Value: round(violationRisk, 3), Unit: "", Color: "#a855f7", Target: 0.7, Status: pickThresholdStatus(violationRisk, 0.7), Max: 1},
				{Key: "errorRate", Label: "错误率", Value: round(errorRate, 3), Unit: "%", Color: "#ef4444", Target: 0.1, Status: pickThresholdStatus(errorRate, 0.1), Max: 1},
			}

			// 错误预算计算
			budgetHours := (100 - 99.5) / 100 * 30 * 24 // 30 天周期
			burnRate := 0.0
			if totalReq > 0 {
				burnRate = float64(totalErr) / float64(totalReq) / ((100 - 99.5) / 100)
			}
			remaining := budgetHours * (1 - clamp(burnRate/30*24, 0, 1))
			sloBudget = types.SLOBudget{
				TotalBudgetPercent:   100,
				RemainingPercent:     round(remaining/budgetHours*100, 1),
				BurnRate:             round(burnRate, 3),
				ProjectedExhaustDate: projectExhaustDate(burnRate),
				Status:               pickBudgetStatus(remaining / budgetHours),
			}
		}

		if len(trendPoints) == 0 && len(statusRows48h) > 0 {
			trendPoints = buildSLOTrendFromStatus(statusRows48h)
		}
		if len(metrics) == 0 && len(serviceStatuses) > 0 {
			metrics = buildSLOMetricsFromStatus(serviceStatuses)
		}
		budgetAllocation = s.allocateSLOBudget(repo, serviceStatuses)
	}

	// 如果 DB 没有数据，降级为静态模拟数据
	if len(trendPoints) == 0 {
		trendPoints = make([]types.SLOTimePoint, 48)
		for i := 47; i >= 0; i-- {
			ts := now.Add(-time.Duration(i) * time.Hour).UnixMilli()
			trendPoints[47-i] = types.SLOTimePoint{
				Timestamp:      ts,
				APIGW:          round(clamp(98.5+rand.Float64()*1.2, 95, 100), 2),
				ModelInference: round(clamp(96.0+rand.Float64()*3.5, 92, 100), 2),
				DataProcess:    round(clamp(99.0+rand.Float64()*0.8, 97, 100), 2),
			}
		}
	}
	if len(metrics) == 0 {
		metrics = []types.SLOMetric{
			{Key: "sloRate", Label: "SLO 达标率", Value: 99.2, Unit: "%", Color: "#0b57d0", Target: 99.5, Status: "warning", Max: 100},
			{Key: "latency", Label: "端到端时延", Value: 142, Unit: "ms", Color: "#3b82f6", Target: 150, Status: "normal", Max: 200},
			{Key: "violationRisk", Label: "违约风险", Value: 0.36, Unit: "", Color: "#a855f7", Target: 0.7, Status: "normal", Max: 1},
			{Key: "errorRate", Label: "错误率", Value: 0.08, Unit: "%", Color: "#ef4444", Target: 0.1, Status: "normal", Max: 1},
		}
	}
	if sloBudget.TotalBudgetPercent == 0 {
		sloBudget = types.SLOBudget{
			TotalBudgetPercent:   100,
			RemainingPercent:     75,
			BurnRate:             0.52,
			ProjectedExhaustDate: "2026-04-15",
			Status:               "normal",
		}
	}
	if statusSummary.TotalCount == 0 {
		serviceStatuses = mockSLOServiceStatuses(now)
		statusSummary = summarizeSLOStatuses(serviceStatuses)
	}
	if len(statusTrend) == 0 {
		statusTrend = mockSLOStatusTrend(now)
	}
	if len(budgetAllocation.Services) == 0 {
		budgetAllocation = mockSLOBudgetAllocation(now, serviceStatuses)
	}
	if len(budgetAllocation.Services) > 0 {
		proxyPrediction = s.buildSLOProxyPrediction(repo, budgetAllocation, serviceStatuses)
	}
	if len(proxyPrediction.Predictions) == 0 {
		proxyPrediction = mockSLOProxyPrediction(now, budgetAllocation, serviceStatuses)
	}

	return &types.SLOResponse{
		Metrics:          metrics,
		Trend48h:         trendPoints,
		SLOBudget:        sloBudget,
		StatusSummary:    statusSummary,
		ServiceStatuses:  serviceStatuses,
		StatusTrend48h:   statusTrend,
		BudgetAllocation: budgetAllocation,
		ProxyPrediction:  proxyPrediction,
	}
}

func convertSLOStatuses(rows []greenrepo.SLOStatusRecord) []types.SLOServiceStatus {
	result := make([]types.SLOServiceStatus, 0, len(rows))
	for _, r := range rows {
		result = append(result, types.SLOServiceStatus{
			ClusterUuid:   r.ClusterUuid,
			ServiceID:     r.ServiceID,
			ServiceName:   firstNonEmpty(r.ServiceName, svcNameToCN(r.ServiceID)),
			APIID:         r.APIID,
			Status:        strings.ToUpper(r.SLOStatus),
			ViolationRisk: round(clamp(r.ViolationRisk, 0, 1), 3),
			Reason:        r.Reason,
			Timestamp:     r.StatusTime.UnixMilli(),
			QPS:           round(r.QPS, 2),
			P95Latency:    round(r.P95Latency, 1),
			P99Latency:    round(r.P99Latency, 1),
			ErrorRate:     round(r.ErrorRate, 3),
			ReplicaCount:  r.ReplicaCount,
			CPUUtil:       round(r.CPUUtil, 1),
			GPUUtil:       round(r.GPUUtil, 1),
			NodePower:     round(r.NodePower, 2),
		})
	}
	return result
}

func summarizeSLOStatuses(rows []types.SLOServiceStatus) types.SLOStatusSummary {
	summary := types.SLOStatusSummary{Status: "NORMAL"}
	for _, r := range rows {
		summary.TotalCount++
		if r.Timestamp > summary.LastUpdated {
			summary.LastUpdated = r.Timestamp
		}
		if r.ViolationRisk > summary.CurrentRisk {
			summary.CurrentRisk = r.ViolationRisk
		}
		switch strings.ToUpper(r.Status) {
		case "VIOLATED":
			summary.ViolatedCount++
		case "WARNING":
			summary.WarningCount++
		default:
			summary.NormalCount++
		}
	}
	switch {
	case summary.ViolatedCount > 0:
		summary.Status = "VIOLATED"
	case summary.WarningCount > 0 || summary.CurrentRisk >= 0.7:
		summary.Status = "WARNING"
	default:
		summary.Status = "NORMAL"
	}
	summary.CurrentRisk = round(summary.CurrentRisk, 3)
	return summary
}

func buildSLOMetricsFromStatus(rows []types.SLOServiceStatus) []types.SLOMetric {
	if len(rows) == 0 {
		return nil
	}
	var p99Sum, errSum, maxRisk float64
	var violated int
	for _, r := range rows {
		p99Sum += r.P99Latency
		errSum += r.ErrorRate
		if r.ViolationRisk > maxRisk {
			maxRisk = r.ViolationRisk
		}
		if strings.ToUpper(r.Status) == "VIOLATED" {
			violated++
		}
	}
	n := float64(len(rows))
	passRate := (n - float64(violated)) / n * 100
	return []types.SLOMetric{
		{Key: "sloRate", Label: "SLO 达标率", Value: round(passRate, 2), Unit: "%", Color: "#0b57d0", Target: 99.5, Status: pickAvailabilityStatus(passRate, 99.5), Max: 100},
		{Key: "latency", Label: "P99 时延", Value: round(p99Sum/n, 0), Unit: "ms", Color: "#3b82f6", Target: 500, Status: pickThresholdStatus(p99Sum/n, 500), Max: 800},
		{Key: "violationRisk", Label: "违约风险", Value: round(maxRisk, 3), Unit: "", Color: "#a855f7", Target: 0.7, Status: pickThresholdStatus(maxRisk, 0.7), Max: 1},
		{Key: "errorRate", Label: "错误率", Value: round(errSum/n, 3), Unit: "%", Color: "#ef4444", Target: 0.1, Status: pickThresholdStatus(errSum/n, 0.1), Max: 1},
	}
}

func buildSLOStatusTrend(rows []greenrepo.SLOStatusRecord) []types.SLOStatusTrendPoint {
	type bucket struct {
		risk     float64
		normal   int
		warning  int
		violated int
	}
	buckets := make(map[int64]*bucket)
	for _, r := range rows {
		ts := r.StatusTime.Truncate(time.Hour).UnixMilli()
		b, ok := buckets[ts]
		if !ok {
			b = &bucket{}
			buckets[ts] = b
		}
		if r.ViolationRisk > b.risk {
			b.risk = r.ViolationRisk
		}
		switch strings.ToUpper(r.SLOStatus) {
		case "VIOLATED":
			b.violated++
		case "WARNING":
			b.warning++
		default:
			b.normal++
		}
	}
	keys := make([]int64, 0, len(buckets))
	for k := range buckets {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	points := make([]types.SLOStatusTrendPoint, 0, len(keys))
	for _, k := range keys {
		b := buckets[k]
		points = append(points, types.SLOStatusTrendPoint{
			Timestamp:     k,
			Risk:          round(b.risk, 3),
			NormalCount:   b.normal,
			WarningCount:  b.warning,
			ViolatedCount: b.violated,
		})
	}
	return points
}

func buildSLOTrendFromStatus(rows []greenrepo.SLOStatusRecord) []types.SLOTimePoint {
	type serviceScores struct {
		apiGW float64
		model float64
		data  float64
	}
	buckets := make(map[int64]*serviceScores)
	for _, r := range rows {
		ts := r.StatusTime.Truncate(time.Hour).UnixMilli()
		b, ok := buckets[ts]
		if !ok {
			b = &serviceScores{apiGW: 99.8, model: 99.5, data: 99.8}
			buckets[ts] = b
		}
		score := round(clamp(100-r.ViolationRisk*2, 90, 100), 2)
		switch r.ServiceID {
		case "api-gw":
			b.apiGW = score
		case "model-inference", "model-infer":
			b.model = score
		case "data-process":
			b.data = score
		}
	}
	keys := make([]int64, 0, len(buckets))
	for k := range buckets {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	points := make([]types.SLOTimePoint, 0, len(keys))
	for _, k := range keys {
		b := buckets[k]
		points = append(points, types.SLOTimePoint{
			Timestamp:      k,
			APIGW:          b.apiGW,
			ModelInference: b.model,
			DataProcess:    b.data,
		})
	}
	return points
}

func mockSLOServiceStatuses(now time.Time) []types.SLOServiceStatus {
	return []types.SLOServiceStatus{
		{ClusterUuid: "demo", ServiceID: "api-gw", ServiceName: "API 网关", APIID: "/api/v1/orders", Status: "NORMAL", ViolationRisk: 0.18, Reason: "所有核心指标处于阈值内", Timestamp: now.UnixMilli(), QPS: 2400, P95Latency: 62, P99Latency: 118, ErrorRate: 0.018, ReplicaCount: 4, CPUUtil: 42, GPUUtil: 0, NodePower: 6.8},
		{ClusterUuid: "demo", ServiceID: "model-inference", ServiceName: "模型推理", APIID: "/api/v1/infer", Status: "WARNING", ViolationRisk: 0.72, Reason: "P99时延接近阈值且GPU利用率偏高", Timestamp: now.UnixMilli(), QPS: 760, P95Latency: 290, P99Latency: 430, ErrorRate: 0.052, ReplicaCount: 3, CPUUtil: 68, GPUUtil: 84, NodePower: 10.6},
		{ClusterUuid: "demo", ServiceID: "data-process", ServiceName: "数据处理", APIID: "/api/v1/features", Status: "NORMAL", ViolationRisk: 0.26, Reason: "吞吐与错误率稳定", Timestamp: now.UnixMilli(), QPS: 1200, P95Latency: 95, P99Latency: 160, ErrorRate: 0.02, ReplicaCount: 3, CPUUtil: 51, GPUUtil: 20, NodePower: 7.2},
	}
}

func mockSLOStatusTrend(now time.Time) []types.SLOStatusTrendPoint {
	points := make([]types.SLOStatusTrendPoint, 48)
	for i := 47; i >= 0; i-- {
		ts := now.Add(-time.Duration(i) * time.Hour).UnixMilli()
		risk := round(clamp(0.2+math.Sin(float64(47-i)/8)*0.12+rand.Float64()*0.18, 0.05, 0.78), 3)
		warning := 0
		if risk >= 0.7 {
			warning = 1
		}
		points[47-i] = types.SLOStatusTrendPoint{
			Timestamp:    ts,
			Risk:         risk,
			NormalCount:  3 - warning,
			WarningCount: warning,
		}
	}
	return points
}

func (s *Service) allocateSLOBudget(repo **greenrepo.Service, statuses []types.SLOServiceStatus) types.SLOBudgetAllocation {
	clusterUuid := "11111111-1111-1111-1111-111111111111"
	for _, status := range statuses {
		if strings.TrimSpace(status.ClusterUuid) != "" && status.ClusterUuid != "demo" {
			clusterUuid = status.ClusterUuid
			break
		}
	}

	objective := defaultSLOObjective(clusterUuid)
	if repo != nil && *repo != nil {
		if dbObjective, err := (*repo).GetActiveSLOObjective(clusterUuid); err == nil && dbObjective != nil {
			objective = *dbObjective
		}
	}
	deps := defaultSLODependencies(objective.ObjectiveID)
	if repo != nil && *repo != nil {
		if dbDeps, err := (*repo).GetSLOServiceDependencies(objective.ObjectiveID); err == nil && len(dbDeps) > 0 {
			deps = dbDeps
		}
	}

	var aggs []greenrepo.SLOServiceMetricAgg
	if repo != nil && *repo != nil {
		if dbAggs, err := (*repo).GetSLOServiceMetricAggLast48h(clusterUuid); err == nil && len(dbAggs) > 0 {
			aggs = dbAggs
		}
	}
	if len(aggs) == 0 {
		aggs = serviceStatusAggs(statuses, clusterUuid)
	}
	allocation := buildSLOBudgetAllocation(objective, deps, aggs)

	if repo != nil && *repo != nil {
		for _, svc := range allocation.Services {
			_ = (*repo).UpsertSLOBudgetAllocation(&greenrepo.SLOBudgetAllocationRecord{
				ObjectiveID:         allocation.ObjectiveID,
				ClusterUuid:         objective.ClusterUuid,
				BusinessFlow:        allocation.BusinessFlow,
				ServiceID:           svc.ServiceID,
				ServiceName:         svc.ServiceName,
				APIID:               svc.APIID,
				AllocationMethod:    allocation.Method,
				LatencyContribution: svc.LatencyContribution,
				QPS:                 svc.QPS,
				QPSCV:               svc.QPSCV,
				ErrorRate:           svc.ErrorRate,
				CriticalPath:        svc.CriticalPath,
				RiskWeight:          svc.RiskWeight,
				BudgetRatio:         svc.BudgetRatio,
				P99LatencyBudget:    svc.P99LatencyBudget,
				ErrorRateBudget:     svc.ErrorRateBudget,
				AllocatedAt:         time.UnixMilli(allocation.GeneratedAt),
			})
		}
	}
	return allocation
}

func defaultSLOObjective(clusterUuid string) greenrepo.SLOObjective {
	return greenrepo.SLOObjective{
		ObjectiveID:      "checkout-flow",
		ClusterUuid:      clusterUuid,
		BusinessFlow:     "下单流程",
		TargetP99Latency: 1000,
		TargetErrorRate:  0.1,
		AllocationMethod: "risk_weighted",
	}
}

func defaultSLODependencies(objectiveID string) []greenrepo.SLOServiceDependency {
	return []greenrepo.SLOServiceDependency{
		{ObjectiveID: objectiveID, ServiceID: "api-gw", ServiceName: "API 网关", APIID: "/api/v1/orders", DownstreamServiceID: "model-inference", IsCritical: true, PathOrder: 1},
		{ObjectiveID: objectiveID, ServiceID: "model-inference", ServiceName: "模型推理", APIID: "/api/v1/infer", UpstreamServiceID: "api-gw", DownstreamServiceID: "data-process", IsCritical: true, PathOrder: 2},
		{ObjectiveID: objectiveID, ServiceID: "data-process", ServiceName: "数据处理", APIID: "/api/v1/features", UpstreamServiceID: "model-inference", IsCritical: false, PathOrder: 3},
	}
}

func serviceStatusAggs(statuses []types.SLOServiceStatus, clusterUuid string) []greenrepo.SLOServiceMetricAgg {
	aggs := make([]greenrepo.SLOServiceMetricAgg, 0, len(statuses))
	for _, status := range statuses {
		aggs = append(aggs, greenrepo.SLOServiceMetricAgg{
			ClusterUuid:  firstNonEmpty(status.ClusterUuid, clusterUuid),
			ServiceID:    status.ServiceID,
			ServiceName:  status.ServiceName,
			APIID:        status.APIID,
			AvgQPS:       status.QPS,
			StdQPS:       status.QPS * status.ViolationRisk * 0.2,
			AvgP99:       status.P99Latency,
			AvgErrorRate: status.ErrorRate,
			SampleCount:  1,
		})
	}
	return aggs
}

func buildSLOBudgetAllocation(objective greenrepo.SLOObjective, deps []greenrepo.SLOServiceDependency, aggs []greenrepo.SLOServiceMetricAgg) types.SLOBudgetAllocation {
	method := strings.TrimSpace(objective.AllocationMethod)
	if method == "" {
		method = "risk_weighted"
	}
	depByKey := make(map[string]greenrepo.SLOServiceDependency)
	for _, dep := range deps {
		depByKey[sloServiceKey(dep.ServiceID, dep.APIID)] = dep
	}
	if len(aggs) == 0 {
		aggs = []greenrepo.SLOServiceMetricAgg{
			{ClusterUuid: objective.ClusterUuid, ServiceID: "api-gw", ServiceName: "API 网关", APIID: "/api/v1/orders", AvgQPS: 2400, StdQPS: 320, AvgP99: 118, AvgErrorRate: 0.018},
			{ClusterUuid: objective.ClusterUuid, ServiceID: "model-inference", ServiceName: "模型推理", APIID: "/api/v1/infer", AvgQPS: 760, StdQPS: 180, AvgP99: 430, AvgErrorRate: 0.052},
			{ClusterUuid: objective.ClusterUuid, ServiceID: "data-process", ServiceName: "数据处理", APIID: "/api/v1/features", AvgQPS: 1200, StdQPS: 150, AvgP99: 160, AvgErrorRate: 0.02},
		}
	}

	totalLatency := 0.0
	for _, agg := range aggs {
		totalLatency += math.Max(agg.AvgP99, 1)
	}
	if totalLatency <= 0 {
		totalLatency = float64(len(aggs))
	}

	type scored struct {
		agg           greenrepo.SLOServiceMetricAgg
		dep           greenrepo.SLOServiceDependency
		contribution  float64
		latencyWeight float64
		errorWeight   float64
		qpsCV         float64
		riskWeight    float64
	}
	scoredRows := make([]scored, 0, len(aggs))
	var latencyWeightSum, errorWeightSum float64
	for _, agg := range aggs {
		dep, ok := depByKey[sloServiceKey(agg.ServiceID, agg.APIID)]
		if !ok {
			dep = greenrepo.SLOServiceDependency{
				ObjectiveID: objective.ObjectiveID,
				ServiceID:   agg.ServiceID,
				ServiceName: agg.ServiceName,
				APIID:       agg.APIID,
			}
		}
		contribution := math.Max(agg.AvgP99, 1) / totalLatency
		qpsCV := 0.0
		if agg.AvgQPS > 1e-9 {
			qpsCV = agg.StdQPS / agg.AvgQPS
		}
		errorRatio := agg.AvgErrorRate / math.Max(objective.TargetErrorRate, 1e-9)
		criticalPenalty := 0.0
		if dep.IsCritical {
			criticalPenalty = 0.25
		}
		latencyWeight := contribution
		errorWeight := 1.0
		riskWeight := 1.0
		if method == "risk_weighted" || method == "improved" {
			latencyWeight = contribution * (1 + clamp(qpsCV, 0, 1)*0.35) / (1 + criticalPenalty + clamp(errorRatio, 0, 3)*0.08)
			errorWeight = 1 / (1 + clamp(errorRatio, 0, 5)*0.8 + criticalPenalty)
			riskWeight = (1 + clamp(qpsCV, 0, 1)*0.35) * (1 + clamp(errorRatio, 0, 3)*0.2 + criticalPenalty)
		}
		latencyWeight = math.Max(latencyWeight, 0.001)
		errorWeight = math.Max(errorWeight, 0.001)
		latencyWeightSum += latencyWeight
		errorWeightSum += errorWeight
		scoredRows = append(scoredRows, scored{
			agg:           agg,
			dep:           dep,
			contribution:  contribution,
			latencyWeight: latencyWeight,
			errorWeight:   errorWeight,
			qpsCV:         qpsCV,
			riskWeight:    riskWeight,
		})
	}

	services := make([]types.SLOServiceBudget, 0, len(scoredRows))
	allocatedLatency := 0.0
	allocatedErrorRate := 0.0
	for _, row := range scoredRows {
		budgetRatio := row.latencyWeight / latencyWeightSum
		errorRatio := row.errorWeight / errorWeightSum
		latencyBudget := objective.TargetP99Latency * budgetRatio
		errorBudget := objective.TargetErrorRate * errorRatio
		allocatedLatency += latencyBudget
		allocatedErrorRate += errorBudget
		services = append(services, types.SLOServiceBudget{
			ServiceID:           row.agg.ServiceID,
			ServiceName:         firstNonEmpty(row.dep.ServiceName, row.agg.ServiceName, row.agg.ServiceID),
			APIID:               firstNonEmpty(row.dep.APIID, row.agg.APIID),
			P99LatencyBudget:    round(latencyBudget, 2),
			ErrorRateBudget:     round(errorBudget, 4),
			BudgetRatio:         round(budgetRatio, 4),
			LatencyContribution: round(row.contribution, 4),
			QPS:                 round(row.agg.AvgQPS, 2),
			QPSCV:               round(row.qpsCV, 4),
			ErrorRate:           round(row.agg.AvgErrorRate, 4),
			CriticalPath:        row.dep.IsCritical,
			RiskWeight:          round(row.riskWeight, 4),
		})
	}
	sort.Slice(services, func(i, j int) bool {
		left := servicePathOrder(deps, services[i].ServiceID, services[i].APIID)
		right := servicePathOrder(deps, services[j].ServiceID, services[j].APIID)
		if left == right {
			return services[i].ServiceID < services[j].ServiceID
		}
		return left < right
	})

	return types.SLOBudgetAllocation{
		ObjectiveID:        objective.ObjectiveID,
		BusinessFlow:       firstNonEmpty(objective.BusinessFlow, "下单流程"),
		Method:             method,
		TargetP99Latency:   round(objective.TargetP99Latency, 2),
		TargetErrorRate:    round(objective.TargetErrorRate, 4),
		AllocatedLatency:   round(allocatedLatency, 2),
		AllocatedErrorRate: round(allocatedErrorRate, 4),
		GeneratedAt:        time.Now().UnixMilli(),
		Services:           services,
	}
}

func sloServiceKey(serviceID, apiID string) string {
	return serviceID + "|" + apiID
}

func servicePathOrder(deps []greenrepo.SLOServiceDependency, serviceID, apiID string) int {
	for _, dep := range deps {
		if dep.ServiceID == serviceID && (dep.APIID == apiID || dep.APIID == "") {
			return dep.PathOrder
		}
	}
	return 999
}

func mockSLOBudgetAllocation(now time.Time, statuses []types.SLOServiceStatus) types.SLOBudgetAllocation {
	objective := defaultSLOObjective("demo")
	return buildSLOBudgetAllocation(objective, defaultSLODependencies(objective.ObjectiveID), serviceStatusAggs(statuses, "demo"))
}

func (s *Service) buildSLOProxyPrediction(repo **greenrepo.Service, allocation types.SLOBudgetAllocation, statuses []types.SLOServiceStatus) types.SLOProxyResponse {
	if repo != nil && *repo != nil {
		if rows, err := (*repo).GetLatestSLOProxyPredictions(); err == nil && len(rows) > 0 {
			filtered := make([]greenrepo.SLOProxyPredictionRecord, 0, len(rows))
			for _, row := range rows {
				if allocation.ObjectiveID == "" || row.ObjectiveID == allocation.ObjectiveID {
					filtered = append(filtered, row)
				}
			}
			if len(filtered) > 0 {
				return convertSLOProxyPredictions(filtered)
			}
		}
	}

	response := heuristicSLOProxyPrediction(allocation, statuses)
	if repo != nil && *repo != nil {
		statusMap := statusByServiceKey(statuses)
		defaultCluster := firstSLOCluster(statuses)
		for _, p := range response.Predictions {
			key := sloServiceKey(p.ServiceID, p.APIID)
			clusterUuid := defaultCluster
			if status, ok := statusMap[key]; ok && strings.TrimSpace(status.ClusterUuid) != "" {
				clusterUuid = status.ClusterUuid
			}
			_ = (*repo).UpsertSLOProxyPrediction(&greenrepo.SLOProxyPredictionRecord{
				ObjectiveID:          p.ObjectiveID,
				ClusterUuid:          clusterUuid,
				BusinessFlow:         p.BusinessFlow,
				ServiceID:            p.ServiceID,
				ServiceName:          p.ServiceName,
				APIID:                p.APIID,
				ForecastTime:         time.UnixMilli(p.ForecastTime),
				HorizonMinutes:       p.HorizonMinutes,
				QPSForecast:          p.QPSForecast,
				RequestMix:           p.RequestMix,
				ReplicaCount:         p.ReplicaCount,
				CPURequest:           p.CPURequest,
				GPURequest:           p.GPURequest,
				MemoryRequestGB:      p.MemoryRequestGB,
				PredictedCPUUtil:     p.PredictedCPUUtil,
				PredictedGPUUtil:     p.PredictedGPUUtil,
				P99LatencyBudget:     p.P99LatencyBudget,
				ErrorRateBudget:      p.ErrorRateBudget,
				PredictedP95Latency:  p.PredictedP95Latency,
				PredictedP99Latency:  p.PredictedP99Latency,
				ViolationProbability: p.ViolationProbability,
				ModelName:            p.ModelName,
				ModelVersion:         p.ModelVersion,
			})
		}
	}
	return response
}

func convertSLOProxyPredictions(rows []greenrepo.SLOProxyPredictionRecord) types.SLOProxyResponse {
	predictions := make([]types.SLOProxyPrediction, 0, len(rows))
	modelName := "slo_proxy_lgbm"
	modelVersion := "v0.3"
	generatedAt := int64(0)
	for _, row := range rows {
		if row.CreatedAt.UnixMilli() > generatedAt {
			generatedAt = row.CreatedAt.UnixMilli()
		}
		modelName = firstNonEmpty(row.ModelName, modelName)
		modelVersion = firstNonEmpty(row.ModelVersion, modelVersion)
		predicted := types.SLOProxyPrediction{
			ObjectiveID:          row.ObjectiveID,
			BusinessFlow:         row.BusinessFlow,
			ServiceID:            row.ServiceID,
			ServiceName:          firstNonEmpty(row.ServiceName, row.ServiceID),
			APIID:                row.APIID,
			ForecastTime:         row.ForecastTime.UnixMilli(),
			HorizonMinutes:       row.HorizonMinutes,
			QPSForecast:          round(row.QPSForecast, 2),
			RequestMix:           row.RequestMix,
			ReplicaCount:         row.ReplicaCount,
			CPURequest:           round(row.CPURequest, 2),
			GPURequest:           round(row.GPURequest, 2),
			MemoryRequestGB:      round(row.MemoryRequestGB, 2),
			PredictedCPUUtil:     round(row.PredictedCPUUtil, 1),
			PredictedGPUUtil:     round(row.PredictedGPUUtil, 1),
			P99LatencyBudget:     round(row.P99LatencyBudget, 2),
			ErrorRateBudget:      round(row.ErrorRateBudget, 4),
			PredictedP95Latency:  round(row.PredictedP95Latency, 1),
			PredictedP99Latency:  round(row.PredictedP99Latency, 1),
			ViolationProbability: round(clamp(row.ViolationProbability, 0, 1), 3),
			ModelName:            firstNonEmpty(row.ModelName, modelName),
			ModelVersion:         firstNonEmpty(row.ModelVersion, modelVersion),
		}
		predicted.CanMeetBudget = proxyCanMeet(predicted)
		predictions = append(predictions, predicted)
	}
	sort.Slice(predictions, func(i, j int) bool {
		if predictions[i].ServiceID == predictions[j].ServiceID {
			return predictions[i].HorizonMinutes < predictions[j].HorizonMinutes
		}
		return predictions[i].ServiceID < predictions[j].ServiceID
	})
	if generatedAt == 0 {
		generatedAt = time.Now().UnixMilli()
	}
	return types.SLOProxyResponse{
		ModelName:    modelName,
		ModelVersion: modelVersion,
		GeneratedAt:  generatedAt,
		Predictions:  predictions,
	}
}

func heuristicSLOProxyPrediction(allocation types.SLOBudgetAllocation, statuses []types.SLOServiceStatus) types.SLOProxyResponse {
	if len(allocation.Services) == 0 {
		return types.SLOProxyResponse{}
	}
	now := time.Now()
	statusMap := statusByServiceKey(statuses)
	predictions := make([]types.SLOProxyPrediction, 0, len(allocation.Services)*3)
	horizons := []int{15, 30, 60}

	for _, budget := range allocation.Services {
		status := statusMap[sloServiceKey(budget.ServiceID, budget.APIID)]
		baseQPS := math.Max(budget.QPS, 1)
		if status.QPS > 0 {
			baseQPS = status.QPS
		}
		baseP95 := status.P95Latency
		if baseP95 <= 0 {
			baseP95 = math.Max(budget.P99LatencyBudget*0.55, 30)
		}
		baseP99 := status.P99Latency
		if baseP99 <= 0 {
			baseP99 = math.Max(baseP95*1.35, budget.P99LatencyBudget*0.75)
		}
		baseError := status.ErrorRate
		if baseError <= 0 {
			baseError = budget.ErrorRate
		}
		replicas := status.ReplicaCount
		if replicas <= 0 {
			replicas = int(math.Max(1, math.Ceil(baseQPS/900)))
		}
		risk := status.ViolationRisk
		if risk <= 0 {
			risk = clamp((budget.RiskWeight-1)*0.45+budget.QPSCV, 0, 1)
		}
		budgetP99 := math.Max(budget.P99LatencyBudget, 1)
		errorBudget := math.Max(budget.ErrorRateBudget, 0.0001)

		for _, horizon := range horizons {
			growth := 1 + float64(horizon)/60*0.08 + risk*0.12 + budget.QPSCV*0.08
			qpsForecast := math.Max(baseQPS*growth, 1)
			replicaCount := replicas
			if risk >= 0.72 && horizon >= 60 {
				replicaCount++
			}

			cpuRequest := clamp(qpsForecast/700, 0.5, 32)
			if budget.CriticalPath {
				cpuRequest *= 1.1
			}
			gpuRequest := 0.0
			serviceText := strings.ToLower(budget.ServiceID + " " + budget.ServiceName)
			switch {
			case strings.Contains(serviceText, "model") || strings.Contains(budget.ServiceName, "模型"):
				gpuRequest = math.Max(1, math.Ceil(qpsForecast/900))
			case strings.Contains(serviceText, "data") || strings.Contains(budget.ServiceName, "数据"):
				gpuRequest = 0.25
			}
			memoryRequestGB := 4 + cpuRequest*1.25 + gpuRequest*12

			cpuCapacity := float64(replicaCount) * 1100 * math.Max(cpuRequest/2, 0.6)
			predictedCPU := clamp(qpsForecast/math.Max(cpuCapacity, 1)*100, 8, 96)
			predictedGPU := 0.0
			if gpuRequest > 0 {
				gpuCapacity := float64(replicaCount) * 850 * gpuRequest
				predictedGPU = clamp(qpsForecast/math.Max(gpuCapacity, 1)*100, 5, 98)
			} else if status.GPUUtil > 0 {
				predictedGPU = clamp(status.GPUUtil*0.35, 0, 35)
			}

			loadFactor := math.Pow(growth, 0.45)
			cpuPressure := math.Max(predictedCPU-70, 0)
			gpuPressure := math.Max(predictedGPU-82, 0)
			resourceFactor := 1 + cpuPressure/100 + gpuPressure/140
			predictedP95 := (baseP95*loadFactor + float64(horizon)*0.2) * resourceFactor
			p99Spread := math.Max(baseP99-baseP95, baseP95*0.25)
			predictedP99 := predictedP95 + p99Spread*math.Sqrt(growth) + cpuPressure*1.4 + gpuPressure*1.8
			latencyPressure := (predictedP99 - budgetP99) / math.Max(budgetP99, 1e-9)
			errorPressure := (baseError - errorBudget) / math.Max(errorBudget, 1e-9)
			capacityPressure := math.Max(predictedCPU-80, 0)/25 + math.Max(predictedGPU-85, 0)/25
			violation := clamp(sigmoid(latencyPressure*4+errorPressure*1.4+capacityPressure*0.45)-0.08, 0.01, 0.99)
			if predictedP99 < budgetP99*0.75 && baseError < errorBudget {
				violation = clamp(violation*0.55, 0.01, 0.99)
			}

			predicted := types.SLOProxyPrediction{
				ObjectiveID:          allocation.ObjectiveID,
				BusinessFlow:         allocation.BusinessFlow,
				ServiceID:            budget.ServiceID,
				ServiceName:          firstNonEmpty(budget.ServiceName, budget.ServiceID),
				APIID:                budget.APIID,
				ForecastTime:         now.Add(time.Duration(horizon) * time.Minute).UnixMilli(),
				HorizonMinutes:       horizon,
				QPSForecast:          round(qpsForecast, 2),
				RequestMix:           proxyRequestMix(budget),
				ReplicaCount:         replicaCount,
				CPURequest:           round(cpuRequest, 2),
				GPURequest:           round(gpuRequest, 2),
				MemoryRequestGB:      round(memoryRequestGB, 2),
				PredictedCPUUtil:     round(predictedCPU, 1),
				PredictedGPUUtil:     round(predictedGPU, 1),
				P99LatencyBudget:     round(budgetP99, 2),
				ErrorRateBudget:      round(errorBudget, 4),
				PredictedP95Latency:  round(predictedP95, 1),
				PredictedP99Latency:  round(predictedP99, 1),
				ViolationProbability: round(violation, 3),
				ModelName:            "slo_proxy_heuristic",
				ModelVersion:         "v0.3_heuristic_baseline",
			}
			predicted.CanMeetBudget = proxyCanMeet(predicted)
			predictions = append(predictions, predicted)
		}
	}

	return types.SLOProxyResponse{
		ModelName:    "slo_proxy_heuristic",
		ModelVersion: "v0.3_heuristic_baseline",
		GeneratedAt:  now.UnixMilli(),
		Predictions:  predictions,
	}
}

func mockSLOProxyPrediction(now time.Time, allocation types.SLOBudgetAllocation, statuses []types.SLOServiceStatus) types.SLOProxyResponse {
	if len(statuses) == 0 {
		statuses = mockSLOServiceStatuses(now)
	}
	if len(allocation.Services) == 0 {
		allocation = mockSLOBudgetAllocation(now, statuses)
	}
	return heuristicSLOProxyPrediction(allocation, statuses)
}

func statusByServiceKey(statuses []types.SLOServiceStatus) map[string]types.SLOServiceStatus {
	result := make(map[string]types.SLOServiceStatus, len(statuses))
	for _, status := range statuses {
		result[sloServiceKey(status.ServiceID, status.APIID)] = status
		if status.APIID == "" {
			result[status.ServiceID+"|"] = status
		}
	}
	return result
}

func firstSLOCluster(statuses []types.SLOServiceStatus) string {
	for _, status := range statuses {
		if strings.TrimSpace(status.ClusterUuid) != "" {
			return status.ClusterUuid
		}
	}
	return "demo"
}

func proxyRequestMix(budget types.SLOServiceBudget) string {
	if budget.CriticalPath {
		return "critical-path"
	}
	if strings.Contains(strings.ToLower(budget.ServiceID), "data") {
		return "feature-batch"
	}
	return "default"
}

func proxyCanMeet(p types.SLOProxyPrediction) bool {
	return p.PredictedP99Latency <= p.P99LatencyBudget && p.ViolationProbability < 0.5
}

func sigmoid(x float64) float64 {
	return 1 / (1 + math.Exp(-x))
}

func pickServiceVal(recs []greenrepo.SLORecord, svc string, ts int64, fallback float64) float64 {
	for _, r := range recs {
		if r.ServiceName == svc {
			return r.SLORate
		}
	}
	return fallback
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func pickStatus(actual, target float64) string {
	// latency: target is threshold, lower is better
	if actual > target {
		return "warning"
	}
	return "normal"
}

func pickThresholdStatus(actual, target float64) string {
	switch {
	case actual >= target:
		return "danger"
	case actual >= target*0.8:
		return "warning"
	default:
		return "normal"
	}
}

func pickAvailabilityStatus(actual, target float64) string {
	switch {
	case actual < target:
		return "danger"
	case actual < target+0.2:
		return "warning"
	default:
		return "normal"
	}
}

func pickBudgetStatus(remaining float64) string {
	switch {
	case remaining < 0.2:
		return "critical"
	case remaining < 0.5:
		return "warning"
	default:
		return "normal"
	}
}

func projectExhaustDate(burnRate float64) string {
	if burnRate <= 0 {
		return "N/A"
	}
	// 剩余 100% 时按当前速率耗尽日期
	hoursLeft := (100.0 / burnRate)
	daysLeft := hoursLeft / 24
	if daysLeft > 365 {
		return "N/A"
	}
	return time.Now().Add(time.Duration(daysLeft) * 24 * time.Hour).Format("2006-01-02")
}

// ===================== Error Budget Service =====================

func (s *Service) GetErrorBudget(repo **greenrepo.Service) *types.ErrorBudgetResponse {
	days := []string{"Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"}

	if repo != nil && *repo != nil {
		agg, err := (*repo).GetAggregatedSLO()
		if err == nil && len(agg) > 0 {
			var totalReq, totalErr int64
			for _, a := range agg {
				totalReq += a.TotalReq
				totalErr += a.TotalErr
			}
			budgetHours := (100 - 99.5) / 100 * 30 * 24
			consumedHours := float64(totalErr) / float64(totalReq+1) / ((100 - 99.5) / 100) * 48 / 30
			remaining := budgetHours - consumedHours
			burnRate := 0.0
			if totalReq > 0 {
				burnRate = float64(totalErr) / float64(totalReq) / ((100 - 99.5) / 100)
			}

			weekly := make([]types.DailyBurnRate, 7)
			for i, d := range days {
				weekly[i] = types.DailyBurnRate{
					Day:      d,
					Consumed: round(consumedHours/7*0.5*(1+rand.Float64()), 3),
				}
			}

			return &types.ErrorBudgetResponse{
				TotalBudgetHours:     round(budgetHours, 2),
				ConsumedHours:        round(consumedHours, 2),
				RemainingHours:       round(remaining, 2),
				BurnRate:             round(burnRate, 3),
				ProjectedExhaustDate: projectExhaustDate(burnRate),
				Status:               pickBudgetStatus(remaining / budgetHours),
				DailyBurnRate:        round(consumedHours/48, 4),
				WeeklyTrend:          weekly,
			}
		}
	}

	// 降级：静态模拟
	weekly := make([]types.DailyBurnRate, 7)
	for i, d := range days {
		weekly[i] = types.DailyBurnRate{
			Day:      d,
			Consumed: round(0.1+rand.Float64()*0.5, 3),
		}
	}
	return &types.ErrorBudgetResponse{
		TotalBudgetHours:     4.32,
		ConsumedHours:        1.08,
		RemainingHours:       3.24,
		BurnRate:             0.52,
		ProjectedExhaustDate: "2026-04-15",
		Status:               "normal",
		DailyBurnRate:        0.125,
		WeeklyTrend:          weekly,
	}
}

// ===================== Resource Service =====================

func (s *Service) GetResource(repo **greenrepo.Service) *types.ResourceResponse {
	now := time.Now()
	var points []types.PowerTrendPoint
	var items []types.ResourceItem

	clusterUuid := "11111111-1111-1111-1111-111111111111"

	if repo != nil && *repo != nil {
		snaps, err := (*repo).GetResourceSnapshotsLast48h(clusterUuid)
		if err == nil && len(snaps) > 0 {
			points = make([]types.PowerTrendPoint, len(snaps))
			var cpuCont, gpuCont, memUse, netBW float64
			for i, snap := range snaps {
				points[i] = types.PowerTrendPoint{
					Timestamp:    snap.SnapshotTime.UnixMilli(),
					ComputePower: round(snap.ComputePower, 2),
					StoragePower: round(snap.StoragePower, 2),
				}
				cpuCont += snap.CPUContention
				gpuCont += snap.GPUContention
				memUse += snap.MemoryUsage
				netBW += snap.NetworkBandwidth
			}
			n := float64(len(snaps))
			items = []types.ResourceItem{
				{Label: "CPU 争用", Value: round(cpuCont/n, 1), Max: 100, Unit: "%", Color: "#0b57d0"},
				{Label: "GPU 争用", Value: round(gpuCont/n, 1), Max: 100, Unit: "%", Color: "#a855f7"},
				{Label: "内存使用", Value: round(memUse/n, 1), Max: 100, Unit: "%", Color: "#f59e0b"},
				{Label: "网络带宽", Value: round(netBW/n, 1), Max: 100, Unit: "%", Color: "#3b82f6"},
			}
		}
	}

	if len(points) == 0 {
		points = make([]types.PowerTrendPoint, 48)
		for i := 47; i >= 0; i-- {
			ts := now.Add(-time.Duration(i) * time.Hour).UnixMilli()
			sinFactor := math.Sin(2 * math.Pi * float64(47-i) / 24)
			points[47-i] = types.PowerTrendPoint{
				Timestamp:    ts,
				ComputePower: round(clamp(820+sinFactor*150+rand.Float64()*80, 500, 1200), 2),
				StoragePower: round(clamp(660+sinFactor*80+rand.Float64()*50, 400, 900), 2),
			}
		}
	}
	if len(items) == 0 {
		items = []types.ResourceItem{
			{Label: "CPU 争用", Value: 34, Max: 100, Unit: "%", Color: "#0b57d0"},
			{Label: "GPU 争用", Value: 67, Max: 100, Unit: "%", Color: "#a855f7"},
			{Label: "内存使用", Value: 78, Max: 100, Unit: "%", Color: "#f59e0b"},
			{Label: "网络带宽", Value: 42, Max: 100, Unit: "%", Color: "#3b82f6"},
		}
	}
	return &types.ResourceResponse{
		Items:         items,
		PowerTrend48h: points,
	}
}

// ===================== Power Storage Service =====================

func (s *Service) GetPowerStorage(repo **greenrepo.Service) *types.PowerStorageResponse {
	now := time.Now()
	var points []types.StorageTrendPoint

	clusterUuid := "11111111-1111-1111-1111-111111111111"

	if repo != nil && *repo != nil {
		snaps, err := (*repo).GetResourceSnapshotsLast48h(clusterUuid)
		if err == nil && len(snaps) > 0 {
			points = make([]types.StorageTrendPoint, len(snaps))
			for i, snap := range snaps {
				points[i] = types.StorageTrendPoint{
					Timestamp:     snap.SnapshotTime.UnixMilli(),
					ComputePower:  round(snap.ComputePower, 2),
					StorageStatus: snap.SOC,
					PVOutput:      round(snap.PVOutput, 2),
				}
			}
		}
	}

	if len(points) == 0 {
		points = make([]types.StorageTrendPoint, 48)
		for i := 47; i >= 0; i-- {
			ts := now.Add(-time.Duration(i) * time.Hour).UnixMilli()
			sinFactor := math.Sin(2 * math.Pi * float64(47-i) / 24)
			points[47-i] = types.StorageTrendPoint{
				Timestamp:     ts,
				ComputePower:  round(clamp(850+sinFactor*160+rand.Float64()*80, 500, 1200), 2),
				StorageStatus: round(clamp(65+sinFactor*8+rand.Float64()*6, 30, 95), 2),
				PVOutput:      round(clamp(300+sinFactor*200+rand.Float64()*100, 0, 600), 2),
			}
		}
	}

	soc, soh, bTemp := 68.0, 95.0, 28.0
	if repo != nil && *repo != nil {
		snap, err := (*repo).GetLatestResourceSnapshot(clusterUuid)
		if err == nil && snap != nil {
			soc = snap.SOC
			soh = snap.SOH
			bTemp = snap.BatteryTemp
		}
	}

	return &types.PowerStorageResponse{
		SOC:                 round(soc, 1),
		SOH:                 round(soh, 1),
		DischargePowerLimit: 500,
		ChargePowerLimit:    400,
		AvailableCapacity:   round(soc, 1),
		BackupRequired:      round(100-soc, 1),
		BatteryTemp:         round(bTemp, 1),
		Status:              pickStorageStatus(soc),
		CurrentMode:         pickStorageMode(soc),
		Trend48h:            points,
	}
}

func pickStorageStatus(soc float64) string {
	switch {
	case soc < 20:
		return "critical"
	case soc < 40:
		return "warning"
	default:
		return "normal"
	}
}

func pickStorageMode(soc float64) string {
	if soc > 80 {
		return "charge"
	}
	if soc < 30 {
		return "discharge"
	}
	return "balanced"
}

// ===================== Service Chain Service =====================

func (s *Service) GetServiceChain(repo **greenrepo.Service) *types.ServiceChainResponse {
	if repo != nil && *repo != nil {
		agg, err := (*repo).GetAggregatedSLO()
		if err == nil && len(agg) > 0 {
			svcs := make([]types.ServiceNode, 0, len(agg))
			for svcName, a := range agg {
				status := "healthy"
				if a.AvgRate < 99 {
					status = "warning"
				}
				if a.AvgRate < 95 {
					status = "critical"
				}
				svcs = append(svcs, types.ServiceNode{
					ID:          fmt.Sprintf("%s-id", svcName),
					Name:        svcNameToCN(svcName),
					Status:      status,
					SLORate:     round(a.AvgRate, 2),
					Latency:     round(a.AvgP95, 0),
					Throughput:  round(a.AvgRate, 1),
					ErrorRate:   round(float64(a.TotalErr)/float64(a.TotalReq+1)*100, 3),
					CPUUsage:    0,
					MemoryUsage: 0,
				})
			}
			if len(svcs) > 0 {
				return &types.ServiceChainResponse{Services: svcs}
			}
		}
	}

	// 降级：静态模拟
	return &types.ServiceChainResponse{
		Services: []types.ServiceNode{
			{ID: "api-gw", Name: "API 网关", Status: "healthy", SLORate: 99.5, Latency: 48, Throughput: 99.1, ErrorRate: 0.02, CPUUsage: 45, MemoryUsage: 62},
			{ID: "model-infer", Name: "模型推理", Status: "warning", SLORate: 98.2, Latency: 230, Throughput: 97.8, ErrorRate: 0.15, CPUUsage: 82, MemoryUsage: 91},
			{ID: "data-process", Name: "数据处理", Status: "healthy", SLORate: 99.8, Latency: 85, Throughput: 99.5, ErrorRate: 0.01, CPUUsage: 55, MemoryUsage: 68},
			{ID: "cache", Name: "缓存服务", Status: "healthy", SLORate: 99.9, Latency: 12, Throughput: 99.8, ErrorRate: 0.01, CPUUsage: 38, MemoryUsage: 72},
			{ID: "storage", Name: "存储服务", Status: "healthy", SLORate: 99.9, Latency: 20, Throughput: 99.3, ErrorRate: 0.03, CPUUsage: 42, MemoryUsage: 55},
		},
	}
}

func svcNameToCN(name string) string {
	switch name {
	case "api-gw":
		return "API 网关"
	case "model-inference":
		return "模型推理"
	case "data-process":
		return "数据处理"
	default:
		return name
	}
}

// ===================== Energy Forecast Service =====================

// StartDataCollector 启动定时采集器，每分钟采集一次指标数据并写入数据库。
func (s *Service) StartDataCollector(repo **greenrepo.Service) {
	nodeUuid := "8f6a1d2e-a234-47af-a0a4-d4c7ef230001"
	clusterUuid := "11111111-1111-1111-1111-111111111111"

	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	// 启动时立即执行一次
	s.collectAndForecast(repo, nodeUuid, clusterUuid)

	for range ticker.C {
		s.collectAndForecast(repo, nodeUuid, clusterUuid)
	}
}

func (s *Service) collectAndForecast(repo **greenrepo.Service, nodeUuid, clusterUuid string) {
	now := time.Now()

	// 生成模拟采集数据
	baseCPU := 58.0 + math.Sin(float64(now.Hour())/24*2*math.Pi)*3
	baseGPU := 48.0 + math.Sin(float64(now.Hour())/24*2*math.Pi)*2.5
	baseMem := 52.0 + math.Sin(float64(now.Hour())/24*2*math.Pi)*2

	cpuUsage := clamp(baseCPU+(rand.Float64()-0.5)*3, 10, 95)
	gpuUsage := clamp(baseGPU+(rand.Float64()-0.5)*2.5, 5, 98)
	memUsage := clamp(baseMem+(rand.Float64()-0.5)*2, 20, 90)
	nodePower := clamp(6.5+cpuUsage/100*3+(rand.Float64()-0.5)*1, 2, 15)

	taskTypes := []string{"ai_infer", "ai_train", "non_ai"}
	taskType := taskTypes[now.Minute()%3]

	m := &greenrepo.Metrics{
		NodeUuid:       nodeUuid,
		ClusterUuid:    clusterUuid,
		MetricTime:     now,
		CPUUsage:       round(cpuUsage, 2),
		GPUUsage:       round(gpuUsage, 2),
		GPUMemoryUsage: round(clamp(gpuUsage*1.05, 0, 100), 2),
		MemoryUsage:    round(memUsage, 2),
		IORead:         round(80+rand.Float64()*120, 2),
		IOWrite:        round(30+rand.Float64()*60, 2),
		NetworkRx:      round(200+rand.Float64()*300, 2),
		NetworkTx:      round(100+rand.Float64()*200, 2),
		NodePower:      round(nodePower, 2),
		UPSPower:       round(1.5+rand.Float64()*2, 2),
		ServiceEnergy:  round(nodePower*0.85, 3),
		TaskType:       taskType,
		BatchSize:      8 + rand.Intn(48),
		Concurrency:    64 + rand.Intn(256),
		RequestRate:    round(3000+rand.Float64()*4000, 2),
		ReplicaCount:   2 + rand.Intn(4),
		SchedulePolicy: "binpack",
	}

	if repo != nil && *repo != nil {
		_ = (*repo).InsertMetric(m)
		s.collectSLOV01(repo, m)
	}

	// 基于最近 60 条历史数据进行 EWMA 预测，并存储预测结果
	var cpuVals []float64
	if repo != nil && *repo != nil {
		metrics, _ := (*repo).GetMetricsByTaskType(taskType, 60)
		for _, me := range metrics {
			cpuVals = append(cpuVals, me.CPUUsage)
		}
	}

	var avgCPU, avgGPU, avgMem, historyStd float64
	if len(cpuVals) > 0 {
		avgCPU = ewmaFromSlice(cpuVals, 0.3)
		avgGPU = clamp(baseGPU+avgCPU-baseCPU, 5, 99)
		avgMem = clamp(baseMem+avgCPU-baseCPU, 20, 95)
		historyStd = stdDevFromSlice(cpuVals)
	} else {
		avgCPU, avgGPU, avgMem = cpuUsage, gpuUsage, memUsage
		historyStd = 5.0
	}

	// 预测未来 1 小时（每分钟一个点，共 60 个）
	forecastLen := 60
	confWidth := getConfidenceWidth(0.95)

	for i := 0; i < forecastLen; i++ {
		ts := now.Add(time.Duration(i+1) * time.Minute)
		t := float64(i + 1)

		trendFactor := 0.05 * t
		sinFactor := math.Sin(2*math.Pi*(float64(60)+t)/1440) * 3

		fCPU := clamp(avgCPU+trendFactor+sinFactor, 10, 98)
		fGPU := clamp(avgGPU+trendFactor*0.7+sinFactor*0.7, 5, 99)
		fMem := clamp(avgMem+trendFactor*0.5+sinFactor*0.5, 20, 95)
		fNodePower := avgCPU/10 + (fCPU+fGPU)/200 + 2

		margin := confWidth * historyStd * math.Sqrt(1+float64(i)*0.02)

		f := &greenrepo.ForecastRecord{
			ForecastTime:           ts,
			NodeUuid:               nodeUuid,
			ClusterUuid:            clusterUuid,
			PredictedCPUUsage:      round(fCPU, 2),
			PredictedGPUUsage:      round(fGPU, 2),
			PredictedMemoryUsage:   round(fMem, 2),
			PredictedNodePower:     round(fNodePower, 2),
			PredictedServiceEnergy: round(fNodePower*0.85, 3),
			UpperBound:             round(clamp(fCPU+margin, 0, 100), 2),
			LowerBound:             round(clamp(fCPU-margin, 0, 100), 2),
			ConfidenceLevel:        0.95,
			TaskType:               taskType,
		}

		if repo != nil && *repo != nil {
			_ = (*repo).InsertForecast(f)
		}
	}
}

type sloServiceProfile struct {
	serviceID   string
	serviceName string
	apiID       string
	qpsShare    float64
	baseLatency float64
	cpuWeight   float64
	gpuWeight   float64
}

func (s *Service) collectSLOV01(repo **greenrepo.Service, nodeMetric *greenrepo.Metrics) {
	if repo == nil || *repo == nil || nodeMetric == nil {
		return
	}
	profiles := []sloServiceProfile{
		{serviceID: "api-gw", serviceName: "API 网关", apiID: "/api/v1/orders", qpsShare: 0.46, baseLatency: 42, cpuWeight: 0.75, gpuWeight: 0.05},
		{serviceID: "model-inference", serviceName: "模型推理", apiID: "/api/v1/infer", qpsShare: 0.22, baseLatency: 180, cpuWeight: 0.45, gpuWeight: 1.35},
		{serviceID: "data-process", serviceName: "数据处理", apiID: "/api/v1/features", qpsShare: 0.32, baseLatency: 76, cpuWeight: 0.65, gpuWeight: 0.25},
	}

	for idx, profile := range profiles {
		replicas := nodeMetric.ReplicaCount
		if replicas <= 0 {
			replicas = 1
		}
		qps := nodeMetric.RequestRate * profile.qpsShare
		qpsPerReplica := qps / float64(replicas)
		pressure := qpsPerReplica / 1200
		p95 := profile.baseLatency +
			nodeMetric.CPUUsage*profile.cpuWeight +
			nodeMetric.GPUUsage*profile.gpuWeight +
			pressure*80 +
			(rand.Float64()-0.5)*18
		p99 := p95*1.28 + pressure*65 + float64(idx)*10
		errorRate := 0.012 +
			math.Max(0, pressure-0.55)*0.08 +
			math.Max(0, nodeMetric.CPUUsage-75)*0.004 +
			math.Max(0, nodeMetric.GPUUsage-82)*0.004 +
			rand.Float64()*0.012

		metric := &greenrepo.SLOMetricTS{
			ClusterUuid:      nodeMetric.ClusterUuid,
			ServiceID:        profile.serviceID,
			ServiceName:      profile.serviceName,
			APIID:            profile.apiID,
			MetricTime:       nodeMetric.MetricTime.Truncate(time.Minute),
			QPS:              round(qps, 2),
			P95Latency:       round(clamp(p95, 1, 2000), 2),
			P99Latency:       round(clamp(p99, 1, 3000), 2),
			ErrorRate:        round(clamp(errorRate, 0, 10), 4),
			ReplicaCount:     replicas,
			CPUUtil:          nodeMetric.CPUUsage,
			GPUUtil:          nodeMetric.GPUUsage,
			NodePower:        nodeMetric.NodePower,
			P99LatencyTarget: 500,
			ErrorRateTarget:  0.1,
		}
		status := analyzeSLOMetric(metric)
		_ = (*repo).InsertSLOMetric(metric)
		_ = (*repo).UpsertSLOStatus(status)

		totalReq := int64(math.Max(metric.QPS*60, 1))
		errorReq := int64(float64(totalReq) * metric.ErrorRate / 100)
		_ = (*repo).InsertSLORecord(&greenrepo.SLORecord{
			ServiceName:   profile.serviceID,
			WindowStart:   metric.MetricTime.Add(-time.Minute),
			WindowEnd:     metric.MetricTime,
			TotalRequests: totalReq,
			ErrorRequests: errorReq,
			P50Latency:    round(metric.P95Latency*0.55, 2),
			P95Latency:    metric.P95Latency,
			P99Latency:    metric.P99Latency,
			SLORate:       round((1-float64(errorReq)/float64(totalReq))*100, 4),
		})
	}
}

func analyzeSLOMetric(m *greenrepo.SLOMetricTS) *greenrepo.SLOStatusRecord {
	p99Target := m.P99LatencyTarget
	if p99Target <= 0 {
		p99Target = 500
	}
	errTarget := m.ErrorRateTarget
	if errTarget <= 0 {
		errTarget = 0.1
	}

	p99Ratio := m.P99Latency / p99Target
	errRatio := m.ErrorRate / errTarget
	resourcePressure := math.Max(m.CPUUtil, m.GPUUtil) / 100
	powerPressure := m.NodePower / 15
	qpsPerReplica := m.QPS / math.Max(float64(m.ReplicaCount), 1)
	qpsPressure := qpsPerReplica / 1200
	risk := clamp(
		0.42*p99Ratio+
			0.28*errRatio+
			0.12*qpsPressure+
			0.10*resourcePressure+
			0.08*powerPressure,
		0,
		1,
	)

	status := "NORMAL"
	reasons := make([]string, 0, 4)
	if m.P99Latency >= p99Target {
		status = "VIOLATED"
		reasons = append(reasons, fmt.Sprintf("P99时延 %.0fms 超过阈值 %.0fms", m.P99Latency, p99Target))
	} else if m.P99Latency >= p99Target*0.8 {
		status = "WARNING"
		reasons = append(reasons, fmt.Sprintf("P99时延达到阈值的 %.0f%%", p99Ratio*100))
	}
	if m.ErrorRate >= errTarget {
		status = "VIOLATED"
		reasons = append(reasons, fmt.Sprintf("错误率 %.3f%% 超过阈值 %.3f%%", m.ErrorRate, errTarget))
	} else if m.ErrorRate >= errTarget*0.8 && status != "VIOLATED" {
		status = "WARNING"
		reasons = append(reasons, fmt.Sprintf("错误率达到阈值的 %.0f%%", errRatio*100))
	}
	if risk >= 0.7 && status == "NORMAL" {
		status = "WARNING"
		reasons = append(reasons, "QPS/资源压力组合显示未来违约风险升高")
	}
	if len(reasons) == 0 {
		reasons = append(reasons, "所有核心指标处于SLO阈值内")
	}

	return &greenrepo.SLOStatusRecord{
		ClusterUuid:   m.ClusterUuid,
		ServiceID:     m.ServiceID,
		ServiceName:   m.ServiceName,
		APIID:         m.APIID,
		StatusTime:    m.MetricTime,
		SLOStatus:     status,
		ViolationRisk: round(risk, 4),
		Reason:        strings.Join(reasons, "；"),
		QPS:           m.QPS,
		P95Latency:    m.P95Latency,
		P99Latency:    m.P99Latency,
		ErrorRate:     m.ErrorRate,
		ReplicaCount:  m.ReplicaCount,
		CPUUtil:       m.CPUUtil,
		GPUUtil:       m.GPUUtil,
		NodePower:     m.NodePower,
	}
}

// GetEnergyForecast 返回基于数据库真实数据的能耗预测结果。
func (s *Service) GetEnergyForecast(repo **greenrepo.Service, hours int, confidenceLevel float64, taskType string) *types.EnergyForecastResponse {
	now := time.Now()

	if hours <= 0 {
		hours = 24
	}
	if confidenceLevel <= 0 {
		confidenceLevel = 0.95
	}

	nodeUuid := "8f6a1d2e-a234-47af-a0a4-d4c7ef230001"

	// 读取历史采集数据
	var historyMetrics []greenrepo.Metrics
	var lastCollectedAt time.Time
	var collectionStatus string

	if repo != nil && *repo != nil {
		filter := taskType
		if filter == "" || filter == "all" {
			filter = "ai_infer"
		}
		dbMetrics, err := (*repo).GetMetricsByTaskType(filter, hours)
		if err == nil && len(dbMetrics) > 0 {
			historyMetrics = dbMetrics
			lastCollectedAt = dbMetrics[0].MetricTime // dbMetrics is DESC, first is newest
			collectionStatus = "collecting"
		}

		// 也检查是否有最新采集
		latest, err := (*repo).GetLatestMetric(nodeUuid)
		if err == nil && latest != nil {
			lastCollectedAt = latest.MetricTime
			if collectionStatus == "" {
				collectionStatus = "idle"
			}
		}
	}

	if len(historyMetrics) == 0 {
		collectionStatus = "no_data"
		// 无 DB 数据时，生成真实感 mock 历史（过去 hours 小时，每小时 1 条）
		mockHistoryCount := hours
		if mockHistoryCount > 720 {
			mockHistoryCount = 720
		}
		historyMetrics = make([]greenrepo.Metrics, 0, mockHistoryCount)
		for i := mockHistoryCount; i > 0; i-- {
			t := now.Add(-time.Duration(i) * time.Hour)
			hOfDay := float64(t.Hour())
			dayOfWeek := float64(t.Weekday())
			// 工作时段峰值 + 周期波动 + 随机噪声
			workBoost := 0.0
			if hOfDay >= 9 && hOfDay <= 18 {
				workBoost = 2.5
			}
			weekendDip := 0.0
			if dayOfWeek == 0 || dayOfWeek == 6 {
				weekendDip = -2.5
			}
			sinAmp := 3.0
			cpu := clamp(58.0+math.Sin(hOfDay/24*2*math.Pi)*sinAmp+workBoost+weekendDip+(rand.Float64()-0.5)*2, 10, 95)
			gpu := clamp(48.0+math.Sin(hOfDay/24*2*math.Pi)*sinAmp*0.8+workBoost*0.8+weekendDip*0.8+(rand.Float64()-0.5)*1.8, 5, 98)
			mem := clamp(52.0+math.Sin(hOfDay/24*2*math.Pi)*sinAmp*0.6+workBoost*0.5+weekendDip*0.5+(rand.Float64()-0.5)*1.5, 20, 90)
			power := clamp(6.5+cpu/100*3+(rand.Float64()-0.5)*1.0, 2, 15)
			historyMetrics = append(historyMetrics, greenrepo.Metrics{
				MetricTime:  t,
				CPUUsage:    round(cpu, 2),
				GPUUsage:    round(gpu, 2),
				MemoryUsage: round(mem, 2),
				NodePower:   round(power, 2),
				TaskType:    "ai_infer",
			})
		}
	}
	sort.Slice(historyMetrics, func(i, j int) bool {
		return historyMetrics[i].MetricTime.Before(historyMetrics[j].MetricTime)
	})

	// 无论 DB 是否有数据，lastCollectedAt 都以最新历史点兜底。
	if len(historyMetrics) > 0 && lastCollectedAt.IsZero() {
		lastCollectedAt = historyMetrics[len(historyMetrics)-1].MetricTime
		if collectionStatus == "" {
			collectionStatus = "idle"
		}
	}

	// 转换为 EnergyDataPoint
	var history []types.EnergyDataPoint
	var cpuVals, gpuVals, memVals, powerVals []float64

	for _, m := range historyMetrics {
		history = append(history, types.EnergyDataPoint{
			Timestamp:   m.MetricTime.UnixMilli(),
			CPUUsage:    round(m.CPUUsage, 2),
			GPUUsage:    round(m.GPUUsage, 2),
			MemoryUsage: round(m.MemoryUsage, 2),
			NodePower:   round(m.NodePower, 2),
			IsForecast:  false,
		})
		cpuVals = append(cpuVals, m.CPUUsage)
		gpuVals = append(gpuVals, m.GPUUsage)
		memVals = append(memVals, m.MemoryUsage)
		powerVals = append(powerVals, m.NodePower)
	}

	// EWMA 平滑 + 预测
	alpha := 0.3
	avgCPU := ewmaFromSlice(cpuVals, alpha)
	avgGPU := ewmaFromSlice(gpuVals, alpha)
	avgMem := ewmaFromSlice(memVals, alpha)
	avgPower := ewmaFromSlice(powerVals, alpha)
	historyStd := stdDevFromSlice(cpuVals)

	if len(cpuVals) == 0 {
		avgCPU, avgGPU, avgMem, avgPower = 60, 50, 55, 8
		historyStd = 5
	}

	confWidth := getConfidenceWidth(confidenceLevel)
	forecastLen := hours
	forecast := make([]types.EnergyDataPoint, forecastLen)
	var totalEnergy float64

	// 以历史最后一个时间点为基准，预测未来
	var lastHistoryTime time.Time
	if len(history) > 0 {
		lastHistoryTime = time.UnixMilli(history[len(history)-1].Timestamp)
	} else {
		lastHistoryTime = now
	}

	for i := 0; i < forecastLen; i++ {
		ts := lastHistoryTime.Add(time.Duration(i+1) * time.Hour)
		// 去掉线性趋势，仅保留小幅日周期波动
		hOfDay := float64(ts.Hour())
		workBoost := 0.0
		if hOfDay >= 9 && hOfDay <= 18 {
			workBoost = 2.0
		}
		periodic := math.Sin(2*math.Pi*hOfDay/24) * 2.5
		noise := (rand.Float64() - 0.5) * 1.5

		fCPU := clamp(avgCPU+workBoost+periodic+noise, 10, 95)
		fGPU := clamp(avgGPU+workBoost*0.8+periodic*0.8+noise*0.8, 5, 98)
		fMem := clamp(avgMem+workBoost*0.5+periodic*0.5+noise*0.5, 20, 90)
		fNodePower := clamp(avgPower+(fCPU-avgCPU)/10+(rand.Float64()-0.5)*0.5, 2, 18)

		// 置信区间固定宽度，不随预测步长膨胀
		margin := clamp(confWidth*historyStd, 1.5, 6.0)

		totalEnergy += fNodePower // kWh (每步 1 小时)

		forecast[i] = types.EnergyDataPoint{
			Timestamp:   ts.UnixMilli(),
			CPUUsage:    round(fCPU, 2),
			GPUUsage:    round(fGPU, 2),
			MemoryUsage: round(fMem, 2),
			NodePower:   round(fNodePower, 2),
			Upper:       round(clamp(fCPU+margin, 0, 100), 2),
			Lower:       round(clamp(fCPU-margin, 0, 100), 2),
			IsForecast:  true,
		}
	}

	carbonEmission := totalEnergy * 0.83 // kg CO2/kWh (电网碳排放因子)

	return &types.EnergyForecastResponse{
		History:          history,
		Forecast:         forecast,
		TotalEnergy:      round(totalEnergy, 2),
		CarbonEmission:   round(carbonEmission, 2),
		AvgCPU:           round(avgCPU, 2),
		AvgGPU:           round(avgGPU, 2),
		AvgPower:         round(avgPower, 2),
		CollectionStatus: collectionStatus,
		LastCollectedAt:  lastCollectedAt,
		ConfidenceLevel:  confidenceLevel,
	}
}

// TriggerCollection 手动触发一次数据采集。
func (s *Service) TriggerCollection(repo **greenrepo.Service) *types.CollectResponse {
	nodeUuid := "8f6a1d2e-a234-47af-a0a4-d4c7ef230001"
	clusterUuid := "11111111-1111-1111-1111-111111111111"
	s.collectAndForecast(repo, nodeUuid, clusterUuid)
	return &types.CollectResponse{
		Success:   true,
		Message:   "数据采集完成",
		Timestamp: time.Now(),
	}
}

// ===================== Helpers =====================

func parseTimeRange(tr string) int {
	switch tr {
	case "1d":
		return 24
	case "7d":
		return 168
	case "30d":
		return 720
	case "90d":
		return 2160
	default:
		return 24
	}
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func round(v float64, prec int) float64 {
	p := math.Pow10(prec)
	return math.Round(v*p) / p
}

func ewmaFromSlice(vals []float64, alpha float64) float64 {
	if len(vals) == 0 {
		return 60
	}
	// 数据通常降序，从最新往前算 EWMA
	s := vals[0]
	for i := 1; i < len(vals); i++ {
		s = alpha*vals[i] + (1-alpha)*s
	}
	return s
}

func stdDevFromSlice(vals []float64) float64 {
	n := float64(len(vals))
	if n < 2 {
		return 5.0
	}
	var sum, sq float64
	for _, v := range vals {
		sum += v
		sq += v * v
	}
	variance := sq/n - (sum/n)*(sum/n)
	if variance < 0 {
		return 5.0
	}
	return math.Sqrt(variance)
}

func getConfidenceWidth(level float64) float64 {
	switch {
	case level >= 0.99:
		return 3.0
	case level >= 0.95:
		return 2.0
	case level >= 0.9:
		return 1.65
	default:
		return 1.28
	}
}

func pickTaskType(filter string) string {
	switch filter {
	case "cpu", "ai_train":
		return "ai_train"
	case "gpu", "ai_infer":
		return "ai_infer"
	case "non_ai":
		return "non_ai"
	default:
		types := []string{"ai_train", "ai_infer", "non_ai"}
		return types[rand.Intn(len(types))]
	}
}
