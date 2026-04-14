#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
RUN_DIR="${ROOT_DIR}/.local/backend"
LOG_DIR="${RUN_DIR}/logs"
PID_DIR="${RUN_DIR}/pids"

mkdir -p "${LOG_DIR}" "${PID_DIR}"

SERVICES=(
  "portal-rpc|application/portal-rpc/portal.go|application/portal-rpc/etc/portal.yaml"
  "manager-rpc|application/manager-rpc/manager.go|application/manager-rpc/etc/manager.yaml"
  "console-rpc|application/console-rpc/console.go|application/console-rpc/etc/console.yaml"
  "portal-api|application/portal-api/portal.go|application/portal-api/etc/portal-api.yaml"
  "manager-api|application/manager-api/manager.go|application/manager-api/etc/manager-api.yaml"
  "workload-api|application/workload-api/workload.go|application/workload-api/etc/workload-api.yaml"
  "console-api|application/console-api/console.go|application/console-api/etc/console-api.yaml"
)

service_port() {
  case "$1" in
    portal-rpc) echo "30010" ;;
    manager-rpc) echo "30011" ;;
    console-rpc) echo "30012" ;;
    portal-api) echo "8810" ;;
    manager-api) echo "8811" ;;
    workload-api) echo "8812" ;;
    console-api) echo "8813" ;;
    *) echo "" ;;
  esac
}

usage() {
  cat <<'EOF'
用法:
  ./scripts/start-backend.sh             # 启动全部后端服务
  ./scripts/start-backend.sh all         # 启动全部后端服务
  ./scripts/start-backend.sh manager-api # 只启动指定服务(可多个)

可选服务:
  portal-rpc manager-rpc console-rpc portal-api manager-api workload-api console-api
EOF
}

service_exists() {
  local target="$1"
  local item name
  for item in "${SERVICES[@]}"; do
    IFS='|' read -r name _ _ <<<"${item}"
    if [[ "${name}" == "${target}" ]]; then
      return 0
    fi
  done
  return 1
}

is_running_by_pid() {
  local pid="$1"
  [[ -n "${pid}" ]] && kill -0 "${pid}" 2>/dev/null
}

listening_pid_by_port() {
  local port="$1"
  lsof -tiTCP:"${port}" -sTCP:LISTEN 2>/dev/null || true
}

start_service() {
  local name="$1"
  local main_rel="$2"
  local cfg_rel="$3"

  local pid_file="${PID_DIR}/${name}.pid"
  local log_file="${LOG_DIR}/${name}.log"
  local main_file="${ROOT_DIR}/${main_rel}"
  local cfg_file="${ROOT_DIR}/${cfg_rel}"
  local local_cfg_file="${cfg_file%.yaml}.local.yaml"

  if [[ ! -f "${main_file}" ]]; then
    echo "[ERROR] ${name}: 未找到入口文件 ${main_file}"
    return 1
  fi
  if [[ ! -f "${cfg_file}" ]]; then
    echo "[ERROR] ${name}: 未找到配置文件 ${cfg_file}"
    return 1
  fi

  if [[ -f "${local_cfg_file}" ]]; then
    cfg_file="${local_cfg_file}"
  fi

  local target_port occupied_pid
  target_port="$(service_port "${name}")"
  if [[ -n "${target_port}" ]]; then
    occupied_pid="$(listening_pid_by_port "${target_port}")"
    if [[ -n "${occupied_pid}" ]]; then
      echo "[ERROR] ${name}: 端口 ${target_port} 已被占用 (pid=${occupied_pid})，请先执行 ./scripts/stop-backend.sh ${name}"
      return 1
    fi
  fi

  if [[ -f "${pid_file}" ]]; then
    local old_pid
    old_pid="$(cat "${pid_file}" 2>/dev/null || true)"
    if is_running_by_pid "${old_pid}"; then
      echo "[SKIP ] ${name}: 已在运行 (pid=${old_pid})"
      return 0
    fi
    rm -f "${pid_file}"
  fi

  echo "[START] ${name} (config=$(basename "${cfg_file}"))"
  (
    cd "${ROOT_DIR}"
    nohup go run "${main_file}" -f "${cfg_file}" >"${log_file}" 2>&1 &
    echo $! >"${pid_file}"
  )

  sleep 1
  local new_pid
  new_pid="$(cat "${pid_file}" 2>/dev/null || true)"
  if ! is_running_by_pid "${new_pid}"; then
    echo "[FAIL ] ${name}: 启动失败，请检查日志 ${log_file}"
    tail -n 20 "${log_file}" 2>/dev/null || true
    rm -f "${pid_file}"
    return 1
  fi

  echo "[ OK  ] ${name}: pid=${new_pid}, log=${log_file}"
}

declare -a targets=()
if [[ $# -eq 0 ]]; then
  targets=("all")
else
  targets=("$@")
fi

if [[ "${targets[0]}" == "-h" || "${targets[0]}" == "--help" ]]; then
  usage
  exit 0
fi

if [[ "${targets[0]}" == "all" ]]; then
  targets=()
  local_item=""
  for local_item in "${SERVICES[@]}"; do
    IFS='|' read -r name _ _ <<<"${local_item}"
    targets+=("${name}")
  done
fi

for target in "${targets[@]}"; do
  if ! service_exists "${target}"; then
    echo "[ERROR] 未知服务: ${target}"
    usage
    exit 1
  fi
done

for item in "${SERVICES[@]}"; do
  IFS='|' read -r name main_rel cfg_rel <<<"${item}"
  for target in "${targets[@]}"; do
    if [[ "${name}" == "${target}" ]]; then
      start_service "${name}" "${main_rel}" "${cfg_rel}"
      break
    fi
  done
done

echo
echo "全部完成。日志目录: ${LOG_DIR}"
echo "查看实时日志示例: tail -f ${LOG_DIR}/manager-api.log"
