from __future__ import annotations

import argparse
from pathlib import Path

from sb3_contrib import MaskablePPO

from rl_envs import (
    CLUSTER_FEATURE_ORDER,
    MAX_CLUSTER_ACTIONS,
    MAX_NODE_ACTIONS,
    NODE_FEATURE_ORDER,
    RandomMaskedScheduleEnv,
)


def create_model(model_path: Path, max_actions: int, feature_order: list[str], seed: int) -> None:
    model_path.parent.mkdir(parents=True, exist_ok=True)
    env = RandomMaskedScheduleEnv(max_actions=max_actions, feature_order=feature_order)
    model = MaskablePPO(
        "MlpPolicy",
        env,
        n_steps=32,
        batch_size=16,
        learning_rate=3e-4,
        ent_coef=0.01,
        verbose=0,
        seed=seed,
    )
    model.save(str(model_path))


def main() -> None:
    parser = argparse.ArgumentParser(description="Create initial random MaskablePPO policy models.")
    parser.add_argument("--models-dir", default="models", help="Directory that stores policy zip files.")
    parser.add_argument("--seed", type=int, default=42)
    args = parser.parse_args()

    models_dir = Path(args.models_dir)
    create_model(models_dir / "cluster_policy.zip", MAX_CLUSTER_ACTIONS, CLUSTER_FEATURE_ORDER, args.seed)
    create_model(models_dir / "node_policy.zip", MAX_NODE_ACTIONS, NODE_FEATURE_ORDER, args.seed + 1)
    print(f"created {models_dir / 'cluster_policy.zip'}")
    print(f"created {models_dir / 'node_policy.zip'}")


if __name__ == "__main__":
    main()
