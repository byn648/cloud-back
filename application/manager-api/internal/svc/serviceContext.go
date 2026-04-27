package svc

import (
	"strings"

	"github.com/yanshicheng/kube-nova/application/manager-api/internal/config"
	"github.com/yanshicheng/kube-nova/application/manager-rpc/client/managerservice"
	"github.com/yanshicheng/kube-nova/application/portal-rpc/client/storageservice"
	"github.com/yanshicheng/kube-nova/application/portal-rpc/client/sysauthservice"
	"github.com/yanshicheng/kube-nova/common/interceptors"
	"github.com/yanshicheng/kube-nova/common/middleware"
	"github.com/yanshicheng/kube-nova/common/verify"
	"github.com/zeromicro/go-zero/core/stores/redis"
	"github.com/zeromicro/go-zero/core/stores/sqlx"
	"github.com/zeromicro/go-zero/rest"
	"github.com/zeromicro/go-zero/zrpc"
)

type ServiceContext struct {
	Config            config.Config
	Cache             *redis.Redis
	DB                sqlx.SqlConn
	Validator         *verify.ValidatorInstance
	JWTAuthMiddleware rest.Middleware
	StoreRpc          storageservice.StorageService
	ManagerRpc        managerservice.ManagerService
	//PrometheusManager *cluster2.PrometheusManager
}

func NewServiceContext(c config.Config) *ServiceContext {
	validator, err := verify.InitValidator(verify.LocaleZH)
	if err != nil {
		panic(err)
	}
	managerRpc := zrpc.MustNewClient(c.ManagerRpc,
		zrpc.WithUnaryClientInterceptor(interceptors.ClientMetadataInterceptor()),
		zrpc.WithUnaryClientInterceptor(interceptors.ClientErrorInterceptor()),
	)
	storeRpc := zrpc.MustNewClient(c.PortalRpc,
		zrpc.WithUnaryClientInterceptor(interceptors.ClientMetadataInterceptor()),
		zrpc.WithUnaryClientInterceptor(interceptors.ClientErrorInterceptor()),
	)
	authRpc := zrpc.MustNewClient(c.PortalRpc,
		zrpc.WithUnaryClientInterceptor(interceptors.ClientMetadataInterceptor()),
		zrpc.WithUnaryClientInterceptor(interceptors.ClientErrorInterceptor()),
	)
	var dbConn sqlx.SqlConn
	if strings.TrimSpace(c.Mysql.DataSource) != "" {
		dbConn = sqlx.NewMysql(c.Mysql.DataSource)
		rawDB, err := dbConn.RawDB()
		if err != nil {
			panic(err)
		}
		if c.Mysql.MaxOpenConns > 0 {
			rawDB.SetMaxOpenConns(c.Mysql.MaxOpenConns)
		}
		if c.Mysql.MaxIdleConns > 0 {
			rawDB.SetMaxIdleConns(c.Mysql.MaxIdleConns)
		}
		if c.Mysql.ConnMaxLifetime > 0 {
			rawDB.SetConnMaxLifetime(c.Mysql.ConnMaxLifetime)
		}
	}
	return &ServiceContext{
		Config:    c,
		Cache:     redis.MustNewRedis(c.Cache),
		DB:        dbConn,
		Validator: validator,
		JWTAuthMiddleware: middleware.NewJWTAuthMiddleware(
			sysauthservice.NewSysAuthService(authRpc)).Handle,
		ManagerRpc: managerservice.NewManagerService(managerRpc),
		StoreRpc:   storageservice.NewStorageService(storeRpc),
		//PrometheusManager: cluster2.NewPrometheusManager(managerservice.NewManagerService(managerRpc)),
	}
}
