package cluster

import (
	"context"
	"errors"
	"strings"

	"github.com/yanshicheng/kube-nova/application/manager-api/internal/svc"
	"github.com/yanshicheng/kube-nova/application/manager-api/internal/types"
	"github.com/yanshicheng/kube-nova/common/handler/errorx"
	"github.com/zeromicro/go-zero/core/logx"
	gosqlx "github.com/zeromicro/go-zero/core/stores/sqlx"
)

type GetClusterMembersLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewGetClusterMembersLogic(ctx context.Context, svcCtx *svc.ServiceContext) *GetClusterMembersLogic {
	return &GetClusterMembersLogic{
		Logger: logx.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

func (l *GetClusterMembersLogic) GetClusterMembers(req *types.GetClusterMembersRequest) ([]types.ClusterMember, error) {
	if _, err := requireClusterMemberSuperAdmin(l.ctx); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.ClusterUuid) == "" {
		return nil, errorx.Msg("集群UUID不能为空")
	}
	if l.svcCtx.DB == nil {
		return nil, errorx.Msg("manager-api 未配置 Mysql，无法查询集群分配用户")
	}

	var clusterCount uint64
	if err := l.svcCtx.DB.QueryRowCtx(
		l.ctx,
		&clusterCount,
		"SELECT COUNT(1) FROM onec_cluster WHERE uuid = ? AND is_deleted = 0",
		req.ClusterUuid,
	); err != nil {
		l.Errorf("查询集群失败 [clusterUuid=%s]: %v", req.ClusterUuid, err)
		return nil, errorx.Msg("查询集群失败")
	}
	if clusterCount == 0 {
		return nil, errorx.Msg("集群不存在")
	}

	type clusterMemberRow struct {
		UserID   uint64 `db:"user_id"`
		Username string `db:"username"`
		Nickname string `db:"nickname"`
	}
	rows := make([]clusterMemberRow, 0)
	query := `
		SELECT
			su.id AS user_id,
			su.username AS username,
			su.nickname AS nickname
		FROM onec_cluster_member ocm
		JOIN sys_user su ON su.id = ocm.user_id
		WHERE ocm.cluster_uuid = ?
			AND ocm.is_deleted = 0
			AND su.is_deleted = 0
		ORDER BY su.username ASC
	`
	if err := l.svcCtx.DB.QueryRowsCtx(l.ctx, &rows, query, req.ClusterUuid); err != nil {
		if isClusterMemberTableMissingErr(err) {
			return nil, errorx.Msg("数据库缺少 onec_cluster_member 表，请执行最新 sql/db.sql")
		}
		if errors.Is(err, gosqlx.ErrNotFound) {
			return []types.ClusterMember{}, nil
		}
		l.Errorf("查询集群分配用户失败 [clusterUuid=%s]: %v", req.ClusterUuid, err)
		return nil, errorx.Msg("查询集群分配用户失败")
	}

	result := make([]types.ClusterMember, 0, len(rows))
	for _, row := range rows {
		result = append(result, types.ClusterMember{
			UserId:   row.UserID,
			Username: row.Username,
			Nickname: row.Nickname,
		})
	}
	return result, nil
}
