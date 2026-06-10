package managerservicelogic

import (
	"context"
	"strings"

	"github.com/yanshicheng/kube-nova/application/manager-rpc/internal/svc"
	"github.com/yanshicheng/kube-nova/application/manager-rpc/pb"
	"github.com/yanshicheng/kube-nova/common/handler/errorx"
	"github.com/zeromicro/go-zero/core/logx"
)

type SchedulePlanSaveLogic struct {
	ctx    context.Context
	svcCtx *svc.ServiceContext
	logx.Logger
}

func NewSchedulePlanSaveLogic(ctx context.Context, svcCtx *svc.ServiceContext) *SchedulePlanSaveLogic {
	return &SchedulePlanSaveLogic{
		ctx:    ctx,
		svcCtx: svcCtx,
		Logger: logx.WithContext(ctx),
	}
}

func (l *SchedulePlanSaveLogic) SchedulePlanSave(in *pb.SaveSchedulePlanReq) (*pb.SaveSchedulePlanResp, error) {
	if strings.TrimSpace(in.GetPlanId()) == "" {
		return nil, errorx.Msg("调度计划ID不能为空")
	}

	status := strings.TrimSpace(in.GetStatus())
	if status == "" {
		status = "PLANNED"
		if !in.GetExecutable() {
			status = "FAILED"
		}
	}

	createdBy := strings.TrimSpace(in.GetCreatedBy())
	updatedBy := strings.TrimSpace(in.GetUpdatedBy())
	if updatedBy == "" {
		updatedBy = createdBy
	}

	query := `
INSERT INTO onec_schedule_plan (
	plan_id, project_id, workspace_id, target_workspace_id, target_project_cluster_id,
	target_cluster_uuid, namespace, service_name, resource_type, resource_name,
	model_version, cluster_snapshot_json, node_snapshot_json, placements_json, plan_json,
	executable, status, reason, created_by, updated_by, is_deleted
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0)
ON DUPLICATE KEY UPDATE
	project_id = VALUES(project_id),
	workspace_id = VALUES(workspace_id),
	target_workspace_id = VALUES(target_workspace_id),
	target_project_cluster_id = VALUES(target_project_cluster_id),
	target_cluster_uuid = VALUES(target_cluster_uuid),
	namespace = VALUES(namespace),
	service_name = VALUES(service_name),
	resource_type = VALUES(resource_type),
	resource_name = VALUES(resource_name),
	model_version = VALUES(model_version),
	cluster_snapshot_json = VALUES(cluster_snapshot_json),
	node_snapshot_json = VALUES(node_snapshot_json),
	placements_json = VALUES(placements_json),
	plan_json = VALUES(plan_json),
	executable = VALUES(executable),
	status = VALUES(status),
	reason = VALUES(reason),
	updated_by = VALUES(updated_by),
	is_deleted = 0`

	_, err := l.svcCtx.Mysql.ExecCtx(
		l.ctx,
		query,
		strings.TrimSpace(in.GetPlanId()),
		in.GetProjectId(),
		in.GetWorkspaceId(),
		in.GetTargetWorkspaceId(),
		in.GetTargetProjectClusterId(),
		strings.TrimSpace(in.GetTargetClusterUuid()),
		strings.TrimSpace(in.GetNamespace()),
		strings.TrimSpace(in.GetServiceName()),
		strings.TrimSpace(in.GetResourceType()),
		strings.TrimSpace(in.GetResourceName()),
		strings.TrimSpace(in.GetModelVersion()),
		in.GetClusterSnapshotJson(),
		in.GetNodeSnapshotJson(),
		in.GetPlacementsJson(),
		in.GetPlanJson(),
		in.GetExecutable(),
		status,
		strings.TrimSpace(in.GetReason()),
		createdBy,
		updatedBy,
	)
	if err != nil {
		l.Errorf("save schedule plan failed: %v", err)
		return nil, errorx.Msg("保存调度计划失败")
	}

	return &pb.SaveSchedulePlanResp{}, nil
}
