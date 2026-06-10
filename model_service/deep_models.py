"""Deep load forecasting models for node, graph, and uncertainty signals."""
from typing import Optional, Sequence

import torch
from torch import nn


class StaticContextEncoder(nn.Module):
    """Encode hardware and deployment context for heterogeneous nodes."""

    def __init__(
        self,
        cat_cardinalities: Optional[Sequence[int]] = None,
        n_numeric_context: int = 0,
        d_model: int = 64,
        dropout: float = 0.1,
    ):
        super().__init__()
        cat_cardinalities = list(cat_cardinalities or [])
        self.cat_embeddings = nn.ModuleList()
        emb_dims = []
        for cardinality in cat_cardinalities:
            num_embeddings = max(int(cardinality), 1)
            emb_dim = min(16, max(4, (num_embeddings + 1) // 2))
            self.cat_embeddings.append(nn.Embedding(num_embeddings, emb_dim))
            emb_dims.append(emb_dim)

        self.numeric_mlp = None
        numeric_dim = 0
        if n_numeric_context > 0:
            numeric_dim = min(32, max(8, d_model // 2))
            self.numeric_mlp = nn.Sequential(
                nn.Linear(n_numeric_context, numeric_dim),
                nn.GELU(),
                nn.Dropout(dropout),
                nn.Linear(numeric_dim, numeric_dim),
                nn.GELU(),
            )

        fusion_in = sum(emb_dims) + numeric_dim
        self.output = None
        if fusion_in > 0:
            self.output = nn.Sequential(
                nn.LayerNorm(fusion_in),
                nn.Dropout(dropout),
                nn.Linear(fusion_in, d_model),
                nn.GELU(),
            )

    def forward(
        self,
        context_cat: Optional[torch.Tensor] = None,
        context_num: Optional[torch.Tensor] = None,
    ) -> Optional[torch.Tensor]:
        pieces = []
        if self.cat_embeddings and context_cat is not None:
            context_cat = context_cat.long()
            for idx, embedding in enumerate(self.cat_embeddings):
                values = context_cat[:, idx].clamp(min=0, max=embedding.num_embeddings - 1)
                pieces.append(embedding(values))
        if self.numeric_mlp is not None and context_num is not None:
            pieces.append(self.numeric_mlp(context_num.float()))
        if not pieces or self.output is None:
            return None
        return self.output(torch.cat(pieces, dim=-1))


class ForecastHeadMixin:
    def _init_forecast_heads(self, d_model: int, pred_len: int, quantiles: Optional[Sequence[float]] = None):
        self.head = nn.Linear(d_model, pred_len)
        self.quantiles = [float(q) for q in (quantiles or [])]
        self.quantile_head = None
        if self.quantiles:
            self.quantile_head = nn.Linear(d_model, pred_len * len(self.quantiles))

    def _forecast_from_tokens(self, target_tokens: torch.Tensor, return_quantiles: bool = False):
        point = self.head(target_tokens).transpose(1, 2)
        if not return_quantiles or self.quantile_head is None:
            return point

        batch, n_targets, _ = target_tokens.shape
        quantile = self.quantile_head(target_tokens)
        quantile = quantile.reshape(batch, n_targets, self.pred_len, len(self.quantiles))
        quantile = quantile.permute(0, 2, 1, 3)
        quantile = torch.sort(quantile, dim=-1).values
        return {"point": point, "quantiles": quantile}


class PatchTST(nn.Module, ForecastHeadMixin):
    """Channel-independent PatchTST-style encoder.

    Input shape: [batch, seq_len, n_features].
    Output shape: [batch, pred_len, n_targets].
    """

    def __init__(
        self,
        n_features: int,
        target_indices: Sequence[int],
        pred_len: int,
        patch_len: int = 16,
        patch_stride: int = 8,
        d_model: int = 64,
        n_heads: int = 4,
        e_layers: int = 2,
        d_ff: int = 128,
        dropout: float = 0.1,
        cat_cardinalities: Optional[Sequence[int]] = None,
        n_numeric_context: int = 0,
        quantiles: Optional[Sequence[float]] = None,
    ):
        super().__init__()
        self.n_features = n_features
        self.target_indices = list(target_indices)
        self.pred_len = pred_len
        self.patch_len = patch_len
        self.patch_stride = patch_stride

        self.patch_proj = nn.Linear(patch_len, d_model)
        encoder_layer = nn.TransformerEncoderLayer(
            d_model=d_model,
            nhead=n_heads,
            dim_feedforward=d_ff,
            dropout=dropout,
            batch_first=True,
            activation="gelu",
        )
        self.encoder = nn.TransformerEncoder(encoder_layer, num_layers=e_layers)
        self.dropout = nn.Dropout(dropout)
        self.context_encoder = StaticContextEncoder(cat_cardinalities, n_numeric_context, d_model, dropout)
        self._init_forecast_heads(d_model, pred_len, quantiles)

    def forward(
        self,
        x: torch.Tensor,
        context_cat: Optional[torch.Tensor] = None,
        context_num: Optional[torch.Tensor] = None,
        return_quantiles: bool = False,
    ) -> torch.Tensor:
        batch, seq_len, n_features = x.shape
        if seq_len < self.patch_len:
            pad_len = self.patch_len - seq_len
            x = torch.nn.functional.pad(x, (0, 0, pad_len, 0))

        patches = x.permute(0, 2, 1).unfold(dimension=-1, size=self.patch_len, step=self.patch_stride)
        batch, n_features, n_patches, _ = patches.shape
        tokens = self.patch_proj(patches.reshape(batch * n_features, n_patches, self.patch_len))
        encoded = self.encoder(tokens)
        pooled = self.dropout(encoded.mean(dim=1)).reshape(batch, n_features, -1)
        target_tokens = pooled[:, self.target_indices, :]
        context = self.context_encoder(context_cat, context_num)
        if context is not None:
            target_tokens = target_tokens + context.unsqueeze(1)
        return self._forecast_from_tokens(target_tokens, return_quantiles)


class ITransformer(nn.Module, ForecastHeadMixin):
    """iTransformer-style inverted token encoder.

    Each variable is a token and its historical trajectory is projected into
    the model dimension before attention models cross-variable relationships.
    """

    def __init__(
        self,
        seq_len: int,
        n_features: int,
        target_indices: Sequence[int],
        pred_len: int,
        d_model: int = 64,
        n_heads: int = 4,
        e_layers: int = 2,
        d_ff: int = 128,
        dropout: float = 0.1,
        cat_cardinalities: Optional[Sequence[int]] = None,
        n_numeric_context: int = 0,
        quantiles: Optional[Sequence[float]] = None,
    ):
        super().__init__()
        self.seq_len = seq_len
        self.n_features = n_features
        self.target_indices = list(target_indices)
        self.pred_len = pred_len

        self.value_embedding = nn.Linear(seq_len, d_model)
        self.variable_embedding = nn.Parameter(torch.zeros(1, n_features, d_model))
        encoder_layer = nn.TransformerEncoderLayer(
            d_model=d_model,
            nhead=n_heads,
            dim_feedforward=d_ff,
            dropout=dropout,
            batch_first=True,
            activation="gelu",
        )
        self.encoder = nn.TransformerEncoder(encoder_layer, num_layers=e_layers)
        self.dropout = nn.Dropout(dropout)
        self.context_encoder = StaticContextEncoder(cat_cardinalities, n_numeric_context, d_model, dropout)
        self._init_forecast_heads(d_model, pred_len, quantiles)
        nn.init.trunc_normal_(self.variable_embedding, std=0.02)

    def forward(
        self,
        x: torch.Tensor,
        context_cat: Optional[torch.Tensor] = None,
        context_num: Optional[torch.Tensor] = None,
        return_quantiles: bool = False,
    ) -> torch.Tensor:
        tokens = self.value_embedding(x.permute(0, 2, 1))
        tokens = tokens + self.variable_embedding[:, : tokens.shape[1], :]
        encoded = self.dropout(self.encoder(tokens))
        target_tokens = encoded[:, self.target_indices, :]
        context = self.context_encoder(context_cat, context_num)
        if context is not None:
            target_tokens = target_tokens + context.unsqueeze(1)
        return self._forecast_from_tokens(target_tokens, return_quantiles)


class EdgeTypedGraphBlock(nn.Module):
    """Small edge-typed graph block used by HeteroSTGNNLoadNet.

    The block keeps the implementation dependency-free while still making edge
    type explicit. It aggregates source node messages into destination nodes
    with a separate projection per edge type.
    """

    def __init__(self, d_model: int, n_edge_types: int, dropout: float = 0.1):
        super().__init__()
        self.edge_linears = nn.ModuleList([nn.Linear(d_model, d_model) for _ in range(n_edge_types)])
        self.norm1 = nn.LayerNorm(d_model)
        self.norm2 = nn.LayerNorm(d_model)
        self.dropout = nn.Dropout(dropout)
        self.ffn = nn.Sequential(
            nn.Linear(d_model, d_model * 2),
            nn.GELU(),
            nn.Dropout(dropout),
            nn.Linear(d_model * 2, d_model),
        )

    def forward(self, graph_tokens: torch.Tensor, edge_index: torch.Tensor, edge_type: torch.Tensor) -> torch.Tensor:
        batch, n_nodes, d_model = graph_tokens.shape
        agg = graph_tokens.new_zeros(batch, n_nodes, d_model)
        deg = graph_tokens.new_zeros(n_nodes)

        src_nodes = edge_index[0]
        dst_nodes = edge_index[1]
        for edge_id in range(edge_index.shape[1]):
            src = int(src_nodes[edge_id])
            dst = int(dst_nodes[edge_id])
            etype = int(edge_type[edge_id])
            agg[:, dst, :] = agg[:, dst, :] + self.edge_linears[etype](graph_tokens[:, src, :])
            deg[dst] = deg[dst] + 1.0

        agg = agg / deg.clamp_min(1.0).view(1, n_nodes, 1)
        hidden = self.norm1(graph_tokens + self.dropout(agg))
        return self.norm2(hidden + self.dropout(self.ffn(hidden)))


class HeteroSTGNNLoadNet(nn.Module, ForecastHeadMixin):
    """Heterogeneous resource-aware spatio-temporal graph load predictor.

    Node, service, and task tokens form a compact heterogeneous graph:
    - node token: multivariate temporal state + hardware/deployment context
    - service token: deployment/service context projected from the same context
    - task token: task context projected from the same context

    This keeps the model trainable with the current node-level collection data,
    while exposing the stage-4 structure needed for node-service-task graph
    modeling once explicit service/task IDs and dependency edges are collected.
    """

    def __init__(
        self,
        seq_len: int,
        n_features: int,
        target_indices: Sequence[int],
        pred_len: int,
        d_model: int = 64,
        n_heads: int = 4,
        e_layers: int = 2,
        d_ff: int = 128,
        dropout: float = 0.1,
        graph_layers: int = 2,
        cat_cardinalities: Optional[Sequence[int]] = None,
        n_numeric_context: int = 0,
        quantiles: Optional[Sequence[float]] = None,
    ):
        super().__init__()
        self.seq_len = seq_len
        self.n_features = n_features
        self.target_indices = list(target_indices)
        self.pred_len = pred_len

        self.value_embedding = nn.Linear(seq_len, d_model)
        self.variable_embedding = nn.Parameter(torch.zeros(1, n_features, d_model))
        encoder_layer = nn.TransformerEncoderLayer(
            d_model=d_model,
            nhead=n_heads,
            dim_feedforward=d_ff,
            dropout=dropout,
            batch_first=True,
            activation="gelu",
        )
        self.temporal_encoder = nn.TransformerEncoder(encoder_layer, num_layers=e_layers)
        self.dropout = nn.Dropout(dropout)
        self.context_encoder = StaticContextEncoder(cat_cardinalities, n_numeric_context, d_model, dropout)

        self.service_proj = nn.Sequential(nn.LayerNorm(d_model), nn.Linear(d_model, d_model), nn.GELU())
        self.task_proj = nn.Sequential(nn.LayerNorm(d_model), nn.Linear(d_model, d_model), nn.GELU())
        self.graph_blocks = nn.ModuleList(
            [EdgeTypedGraphBlock(d_model, n_edge_types=6, dropout=dropout) for _ in range(max(graph_layers, 1))]
        )

        edge_index = torch.tensor(
            [
                [0, 1, 2, 0, 1, 2, 1, 2],
                [0, 1, 2, 1, 0, 0, 2, 1],
            ],
            dtype=torch.long,
        )
        edge_type = torch.tensor([0, 0, 0, 1, 2, 3, 4, 5], dtype=torch.long)
        self.register_buffer("edge_index", edge_index, persistent=False)
        self.register_buffer("edge_type", edge_type, persistent=False)

        self.target_fusion = nn.Sequential(
            nn.LayerNorm(d_model),
            nn.Linear(d_model, d_model),
            nn.GELU(),
            nn.Dropout(dropout),
        )
        self._init_forecast_heads(d_model, pred_len, quantiles)
        nn.init.trunc_normal_(self.variable_embedding, std=0.02)

    def forward(
        self,
        x: torch.Tensor,
        context_cat: Optional[torch.Tensor] = None,
        context_num: Optional[torch.Tensor] = None,
        return_quantiles: bool = False,
    ) -> torch.Tensor:
        tokens = self.value_embedding(x.permute(0, 2, 1))
        tokens = tokens + self.variable_embedding[:, : tokens.shape[1], :]
        encoded = self.dropout(self.temporal_encoder(tokens))
        target_tokens = encoded[:, self.target_indices, :]

        node_token = target_tokens.mean(dim=1)
        context = self.context_encoder(context_cat, context_num)
        if context is not None:
            node_token = node_token + context
            service_token = self.service_proj(context)
            task_token = self.task_proj(context)
        else:
            service_token = self.service_proj(node_token)
            task_token = self.task_proj(node_token)

        graph_tokens = torch.stack([node_token, service_token, task_token], dim=1)
        for block in self.graph_blocks:
            graph_tokens = block(graph_tokens, self.edge_index, self.edge_type)

        refined_node = graph_tokens[:, 0, :]
        fused_targets = self.target_fusion(target_tokens + refined_node.unsqueeze(1))
        return self._forecast_from_tokens(fused_targets, return_quantiles)


def build_deep_model(model_name: str, **kwargs) -> nn.Module:
    name = model_name.lower()
    if name == "patchtst":
        return PatchTST(**kwargs)
    if name in {"itransformer", "iTransformer".lower()}:
        return ITransformer(**kwargs)
    if name in {"heterostgnn", "hetero_stgnn", "hetero-stgnn-loadnet"}:
        return HeteroSTGNNLoadNet(**kwargs)
    raise ValueError(f"Unsupported deep model: {model_name}")
