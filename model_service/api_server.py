"""FastAPI server for model service (V0.1 placeholder - not used in initial integration).

V0.1 uses Go calling predict.py via os/exec. This file is reserved for future
direct FastAPI integration where the Go backend calls this service via HTTP.
"""
from fastapi import FastAPI, HTTPException
from fastapi.middleware.cors import CORSMiddleware
from schemas import PredictConfig, PredictResponse, HistoryResponse, MetadataResponse

app = FastAPI(title="Model Service API", version="0.1.0")

app.add_middleware(
    CORSMiddleware,
    allow_origins=["*"],
    allow_credentials=True,
    allow_methods=["*"],
    allow_headers=["*"],
)


@app.get("/healthz")
async def healthz():
    return {"status": "ok", "service": "model-service"}


@app.post("/predict", response_model=PredictResponse)
async def predict(cfg: PredictConfig):
    raise HTTPException(status_code=501, detail="Use Go handler calling predict.py instead")


@app.get("/history", response_model=HistoryResponse)
async def history(cluster_uuid: str, node_uuid: str, start_time: str, end_time: str):
    raise HTTPException(status_code=501, detail="Use Go handler instead")


@app.get("/metadata", response_model=MetadataResponse)
async def metadata():
    raise HTTPException(status_code=501, detail="Use Go handler instead")


if __name__ == "__main__":
    import uvicorn
    uvicorn.run(app, host="0.0.0.0", port=8890)
