@echo off
chcp 65001 > nul
echo =======================================================
echo         Kube-Nova Backend 一键启动脚本 (Windows)
echo         (后台静默启动版，使用 .local.yaml 配置文件)
echo =======================================================

:: 切换到项目根目录
cd /d "%~dp0.."

echo [0/2] 正在清理可能残留的旧进程...
taskkill /F /IM go.exe /T 2>nul
taskkill /F /IM portal.exe /T 2>nul
taskkill /F /IM manager.exe /T 2>nul
taskkill /F /IM console.exe /T 2>nul
taskkill /F /IM workload.exe /T 2>nul
echo 清理完成！
echo.

:: 创建日志文件夹
if not exist "logs" mkdir logs

echo [1/2] 正在后台启动 RPC 基础服务...
echo 启动 Portal RPC...
start /b cmd /c "go run application/portal-rpc/portal.go -f application/portal-rpc/etc/portal.local.yaml > logs\portal-rpc.log 2>&1"
timeout /t 5 /nobreak > nul

echo 启动 Manager RPC...
start /b cmd /c "go run application/manager-rpc/manager.go -f application/manager-rpc/etc/manager.local.yaml > logs\manager-rpc.log 2>&1"
timeout /t 5 /nobreak > nul

echo 启动 Console RPC...
start /b cmd /c "go run application/console-rpc/console.go -f application/console-rpc/etc/console.local.yaml > logs\console-rpc.log 2>&1"
timeout /t 5 /nobreak > nul

echo [2/2] 正在后台启动 API 网关服务...
echo 启动 Portal API...
start /b cmd /c "go run application/portal-api/portal.go -f application/portal-api/etc/portal-api.local.yaml > logs\portal-api.log 2>&1"
timeout /t 5 /nobreak > nul

echo 启动 Manager API...
start /b cmd /c "go run application/manager-api/manager.go -f application/manager-api/etc/manager-api.local.yaml > logs\manager-api.log 2>&1"
timeout /t 5 /nobreak > nul

echo 启动 Workload API...
start /b cmd /c "go run application/workload-api/workload.go -f application/workload-api/etc/workload-api.local.yaml > logs\workload-api.log 2>&1"
timeout /t 5 /nobreak > nul

echo 启动 Console API...
start /b cmd /c "go run application/console-api/console.go -f application/console-api/etc/console-api.local.yaml > logs\console-api.log 2>&1"

echo.
echo =======================================================
echo 所有服务均已在后台启动！(不会再弹出多余的窗口)
echo 日志已重定向：您可以前往项目根目录下的 logs 文件夹查看每个服务的运行日志。
echo.
echo 【如何彻底停止服务？】
echo 再次运行本脚本时，会自动清理旧进程。
echo 若要单独停止，请在终端运行：
echo taskkill /F /IM go.exe /T
echo =======================================================
echo.
pause
