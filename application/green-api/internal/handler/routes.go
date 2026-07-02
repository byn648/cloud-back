package handler

import (
	"encoding/json"
	"net/http"

	forecasthandler "github.com/yanshicheng/kube-nova/application/green-api/internal/handler/forecast"
	performancehandler "github.com/yanshicheng/kube-nova/application/green-api/internal/handler/performance"
)

type healthzHandler struct{}

func (h *healthzHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	resp := map[string]string{"status": "ok", "service": "green-api"}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.Handle("/healthz", &healthzHandler{})
	mux.HandleFunc("/green/v1/forecast", forecasthandler.ForecastHandler(h.svcCtx))
	mux.HandleFunc("/green/v1/forecast/energy", forecasthandler.EnergyForecastHandler(h.svcCtx))
	mux.HandleFunc("/green/v1/forecast/collect", forecasthandler.EnergyCollectHandler(h.svcCtx))
	mux.HandleFunc("/green/v1/performance/slo", performancehandler.SLOHandler(h.svcCtx))
	mux.HandleFunc("/green/v1/performance/error-budget", performancehandler.ErrorBudgetHandler(h.svcCtx))
	mux.HandleFunc("/green/v1/performance/resource", performancehandler.ResourceHandler(h.svcCtx))
	mux.HandleFunc("/green/v1/performance/power-storage", performancehandler.PowerStorageHandler(h.svcCtx))
	mux.HandleFunc("/green/v1/performance/service-chain", performancehandler.ServiceChainHandler(h.svcCtx))
	mux.HandleFunc("/green/v1/model/predict", ModelPredictHandler(h.svcCtx))
	mux.HandleFunc("/green/v1/model/history", ModelHistoryHandler(h.svcCtx))
	mux.HandleFunc("/green/v1/model/metadata", ModelMetadataHandler(h.svcCtx))
}
