package config

import (
	"time"

	"github.com/zeromicro/go-zero/core/stores/redis"
	"github.com/zeromicro/go-zero/rest"
	"github.com/zeromicro/go-zero/zrpc"
)

type Webhook struct {
	Token string `json:"Token"`
}
type Config struct {
	rest.RestConf
	Cache      redis.RedisConf
	Mysql      MysqlConf
	ManagerRpc zrpc.RpcClientConf
	PortalRpc  zrpc.RpcClientConf
	Webhook    Webhook `json:"Webhook"`
}

type MysqlConf struct {
	DataSource      string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
}
