package cluster

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/yanshicheng/kube-nova/application/manager-api/internal/svc"
	"github.com/yanshicheng/kube-nova/application/manager-api/internal/types"
	"github.com/yanshicheng/kube-nova/common/handler/errorx"
	"github.com/zeromicro/go-zero/core/logx"
	"github.com/zeromicro/go-zero/core/stores/sqlx"
)

type SetClusterMembersLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewSetClusterMembersLogic(ctx context.Context, svcCtx *svc.ServiceContext) *SetClusterMembersLogic {
	return &SetClusterMembersLogic{
		Logger: logx.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

func (l *SetClusterMembersLogic) SetClusterMembers(req *types.SetClusterMembersRequest) (string, error) {
	operator, err := requireClusterMemberSuperAdmin(l.ctx)
	if err != nil {
		return "", err
	}
	clusterUUID := strings.TrimSpace(req.ClusterUuid)
	if clusterUUID == "" {
		return "", errorx.Msg("集群UUID不能为空")
	}
	if l.svcCtx.DB == nil {
		return "", errorx.Msg("manager-api 未配置 Mysql，无法设置集群分配用户")
	}

	var clusterCount uint64
	if err := l.svcCtx.DB.QueryRowCtx(
		l.ctx,
		&clusterCount,
		"SELECT COUNT(1) FROM onec_cluster WHERE uuid = ? AND is_deleted = 0",
		clusterUUID,
	); err != nil {
		l.Errorf("查询集群失败 [clusterUuid=%s]: %v", clusterUUID, err)
		return "", errorx.Msg("查询集群失败")
	}
	if clusterCount == 0 {
		return "", errorx.Msg("集群不存在")
	}

	userIDs := uniquePositiveUserIDs(req.UserIds)
	if err := l.validateUsersExist(userIDs); err != nil {
		return "", err
	}

	if err := l.svcCtx.DB.TransactCtx(l.ctx, func(ctx context.Context, session sqlx.Session) error {
		if _, err := session.ExecCtx(ctx, "DELETE FROM onec_cluster_member WHERE cluster_uuid = ?", clusterUUID); err != nil {
			return err
		}
		insertSQL := `
			INSERT INTO onec_cluster_member (cluster_uuid, user_id, created_by, is_deleted)
			VALUES (?, ?, ?, 0)
		`
		for _, userID := range userIDs {
			if _, err := session.ExecCtx(ctx, insertSQL, clusterUUID, userID, operator); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		if isClusterMemberTableMissingErr(err) {
			return "", errorx.Msg("数据库缺少 onec_cluster_member 表，请执行最新 sql/db.sql")
		}
		l.Errorf("设置集群分配用户失败 [clusterUuid=%s]: %v", clusterUUID, err)
		return "", errorx.Msg("设置集群分配用户失败")
	}

	return "集群分配用户保存成功", nil
}

func (l *SetClusterMembersLogic) validateUsersExist(userIDs []uint64) error {
	if len(userIDs) == 0 {
		return nil
	}

	args := make([]interface{}, 0, len(userIDs))
	placeholders := make([]string, 0, len(userIDs))
	for _, userID := range userIDs {
		placeholders = append(placeholders, "?")
		args = append(args, userID)
	}

	query := fmt.Sprintf(
		"SELECT id FROM sys_user WHERE is_deleted = 0 AND id IN (%s)",
		strings.Join(placeholders, ","),
	)

	type userIDRow struct {
		ID uint64 `db:"id"`
	}
	rows := make([]userIDRow, 0, len(userIDs))
	if err := l.svcCtx.DB.QueryRowsCtx(l.ctx, &rows, query, args...); err != nil {
		l.Errorf("校验用户失败 [userIds=%v]: %v", userIDs, err)
		return errorx.Msg("校验用户失败")
	}

	exists := make(map[uint64]struct{}, len(rows))
	for _, row := range rows {
		exists[row.ID] = struct{}{}
	}

	missing := make([]uint64, 0)
	for _, userID := range userIDs {
		if _, ok := exists[userID]; !ok {
			missing = append(missing, userID)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Slice(missing, func(i, j int) bool {
		return missing[i] < missing[j]
	})
	missingText := make([]string, 0, len(missing))
	for _, userID := range missing {
		missingText = append(missingText, fmt.Sprintf("%d", userID))
	}
	return errorx.Msg("以下用户不存在或已删除: " + strings.Join(missingText, ","))
}
