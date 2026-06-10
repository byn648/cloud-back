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

type RlPolicy struct {
	Enabled            bool    `json:"Enabled,optional"`
	Required           bool    `json:"Required,optional"`
	Endpoint           string  `json:"Endpoint,optional"`
	Timeout            int64   `json:"Timeout,optional"`
	ClusterModel       string  `json:"ClusterModel,optional"`
	NodeModel          string  `json:"NodeModel,optional"`
	DefaultPricePerKwh float64 `json:"DefaultPricePerKwh,optional"`
}

type Config struct {
	rest.RestConf
	Cache      redis.RedisConf
	Mysql      MysqlConf
	ManagerRpc zrpc.RpcClientConf
	PortalRpc  zrpc.RpcClientConf
	Webhook    Webhook  `json:"Webhook"`
	RlPolicy   RlPolicy `json:"RlPolicy,optional"`
}

type MysqlConf struct {
	DataSource      string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
}
