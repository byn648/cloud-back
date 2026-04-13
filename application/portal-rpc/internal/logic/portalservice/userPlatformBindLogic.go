package portalservicelogic

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/yanshicheng/kube-nova/application/portal-rpc/internal/model"
	"github.com/yanshicheng/kube-nova/application/portal-rpc/internal/svc"
	"github.com/yanshicheng/kube-nova/application/portal-rpc/pb"
	"github.com/yanshicheng/kube-nova/common/handler/errorx"

	"github.com/zeromicro/go-zero/core/logx"
	"github.com/zeromicro/go-zero/core/stores/sqlx"
)

type UserPlatformBindLogic struct {
	ctx    context.Context
	svcCtx *svc.ServiceContext
	logx.Logger
}

func NewUserPlatformBindLogic(ctx context.Context, svcCtx *svc.ServiceContext) *UserPlatformBindLogic {
	return &UserPlatformBindLogic{
		ctx:    ctx,
		svcCtx: svcCtx,
		Logger: logx.WithContext(ctx),
	}
}

// -----------------------用户平台权限表-----------------------
// UserPlatformBind 绑定用户平台
func (l *UserPlatformBindLogic) UserPlatformBind(in *pb.BindUserPlatformReq) (*pb.BindUserPlatformResp, error) {
	// 参数验证
	if in.UserId <= 0 {
		l.Errorf("绑定用户平台失败：用户ID无效")
		return nil, errorx.Msg("用户ID无效")
	}
	if len(in.PlatformIds) == 0 {
		l.Errorf("绑定用户平台失败：平台ID列表不能为空")
		return nil, errorx.Msg("平台ID列表不能为空")
	}

	// 使用事务处理
	err := l.svcCtx.SysUserPlatformModel.TransCtx(l.ctx, func(ctx context.Context, session sqlx.Session) error {
		// 1. 先删除该用户的所有平台绑定（软删除）
		existingBindings, err := l.svcCtx.SysUserPlatformModel.SearchNoPage(ctx, "", true, "`user_id` = ?", in.UserId)
		if err != nil && !errors.Is(err, model.ErrNotFound) {
			l.Errorf("查询用户现有平台绑定失败: userId=%d, error=%v", in.UserId, err)
			return errorx.Msg("查询用户现有平台绑定失败")
		}

		// 软删除现有绑定
		for _, binding := range existingBindings {
			if binding.IsDeleted == 0 {
				binding.IsDeleted = 1
				binding.UpdateTime = time.Now()
				if in.CreateBy != "" {
					binding.UpdateBy = sql.NullString{String: in.CreateBy, Valid: true}
				}
				if err := l.svcCtx.SysUserPlatformModel.Update(ctx, binding); err != nil {
					l.Errorf("删除用户平台绑定失败: userId=%d, platformId=%d, error=%v", in.UserId, binding.PlatformId, err)
					return errorx.Msg("删除用户平台绑定失败")
				}
			}
		}

		// 2. 保存新的平台绑定
		// 说明：
		// sys_user_platform 存在唯一键 (user_id, platform_id)。
		// 不能简单“软删除后再插入”，否则会触发重复键冲突。
		// 正确做法是：存在记录则恢复/更新，不存在才插入。
		for _, platformId := range in.PlatformIds {
			// 验证平台是否存在
			platform, err := l.svcCtx.SysPlatformModel.FindOne(ctx, platformId)
			if err != nil {
				if errors.Is(err, model.ErrNotFound) {
					l.Errorf("绑定用户平台失败：平台不存在, platformId=%d", platformId)
					return errorx.Msg(fmt.Sprintf("平台不存在: %d", platformId))
				}
				l.Errorf("查询��台失败: platformId=%d, error=%v", platformId, err)
				return errorx.Msg("查询平台失败")
			}

			existingBinding, err := l.svcCtx.SysUserPlatformModel.FindOneByUserIdPlatformId(ctx, in.UserId, platformId)
			if err != nil && !errors.Is(err, model.ErrNotFound) {
				l.Errorf("查询用户平台绑定失败: userId=%d, platformId=%d, error=%v", in.UserId, platformId, err)
				return errorx.Msg("查询用户平台绑定失败")
			}

			// 若存在历史记录（包括已软删除），直接恢复并更新，避免唯一键冲突
			if err == nil && existingBinding != nil {
				existingBinding.IsEnable = platform.IsEnable
				existingBinding.Status = 1
				existingBinding.IsDeleted = 0
				existingBinding.UpdateTime = time.Now()
				if in.CreateBy != "" {
					existingBinding.UpdateBy = sql.NullString{String: in.CreateBy, Valid: true}
					if !existingBinding.CreateBy.Valid {
						existingBinding.CreateBy = sql.NullString{String: in.CreateBy, Valid: true}
					}
				}

				if err := l.svcCtx.SysUserPlatformModel.Update(ctx, existingBinding); err != nil {
					l.Errorf("更新用户平台绑定失败: userId=%d, platformId=%d, error=%v", in.UserId, platformId, err)
					return errorx.Msg("更新用户平台绑定失败")
				}
				continue
			}

			// 不存在历史记录，执行插入
			userPlatform := &model.SysUserPlatform{
				UserId:     in.UserId,
				PlatformId: platformId,
				IsEnable:   platform.IsEnable, // 继承平台的启用状态
				Status:     1,                 // 默认启用
				IsDeleted:  0,
				CreateTime: time.Now(),
				UpdateTime: time.Now(),
			}

			if in.CreateBy != "" {
				userPlatform.CreateBy = sql.NullString{String: in.CreateBy, Valid: true}
				userPlatform.UpdateBy = sql.NullString{String: in.CreateBy, Valid: true}
			}

			// 插入数据库
			_, err = l.svcCtx.SysUserPlatformModel.Insert(ctx, userPlatform)
			if err != nil {
				l.Errorf("插入用户平台绑定失败: userId=%d, platformId=%d, error=%v", in.UserId, platformId, err)
				return errorx.Msg("插入用户平台绑定失败")
			}
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	l.Infof("绑定用户平台成功: userId=%d, platformIds=%v", in.UserId, in.PlatformIds)
	return &pb.BindUserPlatformResp{}, nil
}
