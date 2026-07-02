package forecast

import (
	"encoding/json"
	"net/http"
	"strconv"

	greenlogic "github.com/yanshicheng/kube-nova/application/green-api/internal/logic/forecast"
	"github.com/yanshicheng/kube-nova/application/green-api/internal/svc"
	"github.com/yanshicheng/kube-nova/application/green-api/internal/types"
)

func ForecastHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid query parameters", http.StatusBadRequest)
			return
		}

		req := types.ForecastRequest{
			TaskType:   r.FormValue("taskType"),
			Server:     r.FormValue("server"),
			Service:    r.FormValue("service"),
			Instance:   r.FormValue("instance"),
			TimeRange:  r.FormValue("timeRange"),
			DataSource: r.FormValue("dataSource"),
		}

		if v := r.FormValue("confidenceLevel"); v != "" {
			req.ConfidenceLevel, _ = strconv.ParseFloat(v, 64)
		}
		if v := r.FormValue("forecastHorizon"); v != "" {
			req.ForecastHorizon, _ = strconv.Atoi(v)
		}
		if v := r.FormValue("smoothingFactor"); v != "" {
			req.SmoothingFactor, _ = strconv.ParseFloat(v, 64)
		}
		if v := r.FormValue("refreshInterval"); v != "" {
			req.RefreshInterval, _ = strconv.Atoi(v)
		}

		l := greenlogic.NewForecastLogic(r.Context(), svcCtx)
		resp := l.GetForecast(&req)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}
}

// EnergyForecastHandler 返回基于数据库真实数据的能耗预测。
func EnergyForecastHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid query parameters", http.StatusBadRequest)
			return
		}

		req := types.EnergyForecastRequest{
			TaskType: r.FormValue("taskType"),
		}

		if v := r.FormValue("hours"); v != "" {
			req.Hours, _ = strconv.Atoi(v)
		}
		if v := r.FormValue("confidenceLevel"); v != "" {
			req.ConfidenceLevel, _ = strconv.ParseFloat(v, 64)
		}

		resp := svcCtx.Green.GetEnergyForecast(&svcCtx.GreenRepo, req.Hours, req.ConfidenceLevel, req.TaskType)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}
}

// EnergyCollectHandler 手动触发一次数据采集。
func EnergyCollectHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		resp := svcCtx.Green.TriggerCollection(&svcCtx.GreenRepo)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}
}
