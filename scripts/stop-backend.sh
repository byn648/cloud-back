#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
RUN_DIR="${ROOT_DIR}/.local/backend"
PID_DIR="${RUN_DIR}/pids"

SERVICES=(
  "portal-rpc|application/portal-rpc/portal.go"
  "manager-rpc|application/manager-rpc/manager.go"
  "console-rpc|application/console-rpc/console.go"
  "portal-api|application/portal-api/portal.go"
  "manager-api|application/manager-api/manager.go"
  "workload-api|application/workload-api/workload.go"
  "console-api|application/console-api/console.go"
)

usage() {
  cat <<'EOF'
用法:
  ./scripts/stop-backend.sh             # 停止全部后端服务
  ./scripts/stop-backend.sh all         # 停止全部后端服务
  ./scripts/stop-backend.sh manager-api # 只停止指定服务(可多个)

可选服务:
  portal-rpc manager-rpc console-rpc portal-api manager-api workload-api console-api
EOF
}

service_exists() {
  local target="$1"
  local item name
  for item in "${SERVICES[@]}"; do
    IFS='|' read -r name _ <<<"${item}"
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

stop_pid_gracefully() {
  local pid="$1"
  local name="$2"

  if ! is_running_by_pid "${pid}"; then
    return 0
  fi

  kill "${pid}" 2>/dev/null || true

  local i=0
  while [[ ${i} -lt 10 ]]; do
    if ! is_running_by_pid "${pid}"; then
      return 0
    fi
    sleep 1
    i=$((i + 1))
  done

  echo "[WARN ] ${name}: 进程 ${pid} 超时未退出，执行强制停止"
  kill -9 "${pid}" 2>/dev/null || true
}

stop_service() {
  local name="$1"
  local main_rel="$2"

  local pid_file="${PID_DIR}/${name}.pid"
  local main_file="${ROOT_DIR}/${main_rel}"
  local stopped="false"

  if [[ -f "${pid_file}" ]]; then
    local pid
    pid="$(cat "${pid_file}" 2>/dev/null || true)"
    if is_running_by_pid "${pid}"; then
      echo "[STOP ] ${name}: pid=${pid}"
      stop_pid_gracefully "${pid}" "${name}"
      stopped="true"
    fi
    rm -f "${pid_file}"
  fi

  # pid 文件丢失或过期时的兜底：按命令行匹配当前仓库路径
  local found_pids
  found_pids="$(pgrep -f "${main_file}" || true)"
  if [[ -n "${found_pids}" ]]; then
    local pid
    for pid in ${found_pids}; do
      echo "[STOP ] ${name}: pid=${pid} (matched by command line)"
      stop_pid_gracefully "${pid}" "${name}"
      stopped="true"
    done
  fi

  if [[ "${stopped}" == "true" ]]; then
    echo "[ OK  ] ${name}: 已停止"
  else
    echo "[SKIP ] ${name}: 未运行"
  fi
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
    IFS='|' read -r name _ <<<"${local_item}"
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
  IFS='|' read -r name main_rel <<<"${item}"
  for target in "${targets[@]}"; do
    if [[ "${name}" == "${target}" ]]; then
      stop_service "${name}" "${main_rel}"
      break
    fi
  done
done

echo
echo "全部完成。"
