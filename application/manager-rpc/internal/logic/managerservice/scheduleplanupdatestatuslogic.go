package managerservicelogic

import (
	"context"
	"strings"

	"github.com/yanshicheng/kube-nova/application/manager-rpc/internal/svc"
	"github.com/yanshicheng/kube-nova/application/manager-rpc/pb"
	"github.com/yanshicheng/kube-nova/common/handler/errorx"
	"github.com/zeromicro/go-zero/core/logx"
)

type SchedulePlanUpdateStatusLogic struct {
	ctx    context.Context
	svcCtx *svc.ServiceContext
	logx.Logger
}

func NewSchedulePlanUpdateStatusLogic(ctx context.Context, svcCtx *svc.ServiceContext) *SchedulePlanUpdateStatusLogic {
	return &SchedulePlanUpdateStatusLogic{
		ctx:    ctx,
		svcCtx: svcCtx,
		Logger: logx.WithContext(ctx),
	}
}

func (l *SchedulePlanUpdateStatusLogic) SchedulePlanUpdateStatus(in *pb.UpdateSchedulePlanStatusReq) (*pb.UpdateSchedulePlanStatusResp, error) {
	planId := strings.TrimSpace(in.GetPlanId())
	status := strings.TrimSpace(in.GetStatus())
	if planId == "" {
		return nil, errorx.Msg("调度计划ID不能为空")
	}
	if status == "" {
		return nil, errorx.Msg("调度计划状态不能为空")
	}

	query := `
UPDATE onec_schedule_plan
SET status = ?,
	execute_message = ?,
	resource_type = IF(? = '', resource_type, ?),
	resource_name = IF(? = '', resource_name, ?),
	updated_by = IF(? = '', updated_by, ?)
WHERE plan_id = ? AND is_deleted = 0`

	_, err := l.svcCtx.Mysql.ExecCtx(
		l.ctx,
		query,
		status,
		strings.TrimSpace(in.GetExecuteMessage()),
		strings.TrimSpace(in.GetResourceType()),
		strings.TrimSpace(in.GetResourceType()),
		strings.TrimSpace(in.GetResourceName()),
		strings.TrimSpace(in.GetResourceName()),
		strings.TrimSpace(in.GetUpdatedBy()),
		strings.TrimSpace(in.GetUpdatedBy()),
		planId,
	)
	if err != nil {
		l.Errorf("update schedule plan status failed: %v", err)
		return nil, errorx.Msg("更新调度计划状态失败")
	}

	return &pb.UpdateSchedulePlanStatusResp{}, nil
}
