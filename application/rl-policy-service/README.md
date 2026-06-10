# RL Policy Service

This service is the deep reinforcement learning policy inference endpoint used by the Go scheduler.

Models:

- `maskable-ppo-cluster-v1`: chooses the target cluster.
- `maskable-ppo-node-v1`: chooses one node for one replica placement step.

The implementation uses `sb3-contrib` `MaskablePPO`, so the Go scheduler can pass an `actionMask`
that disables infeasible clusters or nodes.

Run locally:

```bash
pip install -r requirements.txt
uvicorn app:app --host 0.0.0.0 --port 18080
```

For local smoke tests before trained artifacts are available, create placeholder random models:

```bash
python init_models.py
```

Default model files:

```text
models/cluster_policy.zip
models/node_policy.zip
```

In normal use these files should be trained offline and then copied into `models/`. The online
scheduler only performs inference; it does not collect post-deployment reward and does not update
the model.

If the model files are not present, the service returns HTTP 503 by default. Set
`RL_POLICY_ALLOW_FALLBACK=true` only for temporary debugging if you explicitly want first-allowed
fallback behavior.

Go scheduler endpoints:

```text
POST /v1/predict/cluster
POST /v1/predict/node
```

The scheduler page should display only the decoded scheduling result: target cluster, node placement,
plan id, model version, and execution method. It should not display policy logits, scores, action
probabilities, or ranking.
