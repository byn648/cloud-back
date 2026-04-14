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

service_cfgs() {
  case "$1" in
    portal-rpc) echo "application/portal-rpc/etc/portal.yaml application/portal-rpc/etc/portal.local.yaml" ;;
    manager-rpc) echo "application/manager-rpc/etc/manager.yaml application/manager-rpc/etc/manager.local.yaml" ;;
    console-rpc) echo "application/console-rpc/etc/console.yaml application/console-rpc/etc/console.local.yaml" ;;
    portal-api) echo "application/portal-api/etc/portal-api.yaml application/portal-api/etc/portal-api.local.yaml" ;;
    manager-api) echo "application/manager-api/etc/manager-api.yaml application/manager-api/etc/manager-api.local.yaml" ;;
    workload-api) echo "application/workload-api/etc/workload-api.yaml application/workload-api/etc/workload-api.local.yaml" ;;
    console-api) echo "application/console-api/etc/console-api.yaml application/console-api/etc/console-api.local.yaml" ;;
    *) echo "" ;;
  esac
}

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

  # pid 文件丢失或过期时的兜底 1：按入口文件路径匹配
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

  # pid 文件丢失或过期时的兜底 2：按配置文件路径匹配（go run 二进制进程常见）
  local cfg_rel cfg_file
  for cfg_rel in $(service_cfgs "${name}"); do
    cfg_file="${ROOT_DIR}/${cfg_rel}"
    found_pids="$(pgrep -f "${cfg_file}" || true)"
    if [[ -n "${found_pids}" ]]; then
      local pid
      for pid in ${found_pids}; do
        echo "[STOP ] ${name}: pid=${pid} (matched by config path)"
        stop_pid_gracefully "${pid}" "${name}"
        stopped="true"
      done
    fi
  done

  # pid 文件丢失或过期时的兜底 3：按监听端口杀进程
  local port
  port="$(service_port "${name}")"
  if [[ -n "${port}" ]]; then
    found_pids="$(lsof -tiTCP:${port} -sTCP:LISTEN 2>/dev/null || true)"
    if [[ -n "${found_pids}" ]]; then
      local pid
      for pid in ${found_pids}; do
        echo "[STOP ] ${name}: pid=${pid} (matched by listen port ${port})"
        stop_pid_gracefully "${pid}" "${name}"
        stopped="true"
      done
    fi
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
