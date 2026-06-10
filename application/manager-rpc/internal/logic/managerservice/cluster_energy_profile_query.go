package managerservicelogic

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/yanshicheng/kube-nova/application/manager-rpc/pb"
	"github.com/zeromicro/go-zero/core/stores/sqlx"
)

// clusterEnergyData 来自表 onec_cluster_energy_profile
type clusterEnergyData struct {
	HasRow bool
	Grid   float64
	HasSoc bool
	Soc    float64
}

func loadClusterEnergyMapByUuids(ctx context.Context, conn sqlx.SqlConn, uuids []string) (map[string]clusterEnergyData, error) {
	seen := make(map[string]bool)
	uniq := make([]string, 0, len(uuids))
	for _, u := range uuids {
		if u == "" || seen[u] {
			continue
		}
		seen[u] = true
		uniq = append(uniq, u)
	}
	if len(uniq) == 0 {
		return map[string]clusterEnergyData{}, nil
	}
	placeholders := make([]string, len(uniq))
	args := make([]any, 0, len(uniq))
	for i := range uniq {
		placeholders[i] = "?"
		args = append(args, uniq[i])
	}
	q := fmt.Sprintf(
		"SELECT cluster_uuid, grid_price_per_kwh, storage_soc FROM onec_cluster_energy_profile WHERE is_deleted = 0 AND cluster_uuid IN (%s)",
		strings.Join(placeholders, ","),
	)
	var rows []struct {
		ClusterUuid string         `db:"cluster_uuid"`
		Grid        sql.NullFloat64 `db:"grid_price_per_kwh"`
		Soc         sql.NullFloat64 `db:"storage_soc"`
	}
	if err := conn.QueryRowsCtx(ctx, &rows, q, args...); err != nil {
		return nil, err
	}
	m := make(map[string]clusterEnergyData, len(rows))
	for _, r := range rows {
		ed := clusterEnergyData{HasRow: true, HasSoc: r.Soc.Valid}
		if r.Grid.Valid {
			ed.Grid = r.Grid.Float64
		}
		if r.Soc.Valid {
			ed.Soc = r.Soc.Float64
		}
		m[r.ClusterUuid] = ed
	}
	return m, nil
}

func mergeProjectClusterEnergy(p *pb.OnecProjectCluster, d clusterEnergyData) {
	if p == nil || !d.HasRow {
		return
	}
	p.HasEnergyProfile = true
	p.GridPricePerKwh = d.Grid
	p.HasStorageSoc = d.HasSoc
	if d.HasSoc {
		p.StorageSoc = d.Soc
	} else {
		p.StorageSoc = 0
	}
}
