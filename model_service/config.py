"""Configuration management for model service."""
import os
from pathlib import Path
from dotenv import load_dotenv
import pymysql.cursors

load_dotenv()

BASE_DIR = Path(__file__).resolve().parent
CHECKPOINTS_DIR = BASE_DIR / "checkpoints"
CHECKPOINTS_DIR.mkdir(parents=True, exist_ok=True)

MYSQL_HOST = os.getenv("MYSQL_HOST", "127.0.0.1")
MYSQL_PORT = int(os.getenv("MYSQL_PORT", "3306"))
MYSQL_USER = os.getenv("MYSQL_USER", "root")
MYSQL_PASSWORD = os.getenv("MYSQL_PASSWORD", "123456")
MYSQL_DATABASE = os.getenv("MYSQL_DATABASE", "cloud_back")

DB_CONFIG = {
    "host": MYSQL_HOST,
    "port": MYSQL_PORT,
    "user": MYSQL_USER,
    "password": MYSQL_PASSWORD,
    "database": MYSQL_DATABASE,
    "charset": "utf8mb4",
    "cursorclass": pymysql.cursors.DictCursor,
}

DEFAULT_HORIZONS = [15, 30, 60]
DEFAULT_HISTORY_WINDOW = 60
DEFAULT_RESAMPLE_FREQ = "1min"
DEFAULT_MAX_MISSING_RATIO = 0.3
MODEL_VERSION = "lightgbm_v0.1"
PYTHON_BIN = os.getenv("PYTHON_BIN", "python3")
MODEL_SERVICE_DIR = str(BASE_DIR)
