package config

// AppConfig keeps backend configuration in a kube-nova-like style.
type AppConfig struct {
	Name    string `yaml:"Name"`
	Host    string `yaml:"Host"`
	Port    int    `yaml:"Port"`
	Timeout int    `yaml:"Timeout"`

	Auth AuthConfig `yaml:"Auth"`

	Mysql MysqlConfig `yaml:"Mysql"`
	Redis RedisConfig `yaml:"Redis"`
	Minio MinioConfig `yaml:"Minio"`
	K8s   K8sConfig   `yaml:"K8s"`
}

type AuthConfig struct {
	AccessExpiresIn  int64 `yaml:"AccessExpiresIn"`
	RefreshExpiresIn int64 `yaml:"RefreshExpiresIn"`
}

type MysqlConfig struct {
	Enabled    bool   `yaml:"Enabled"`
	DataSource string `yaml:"DataSource"`
}

type RedisConfig struct {
	Enabled  bool   `yaml:"Enabled"`
	Addr     string `yaml:"Addr"`
	Password string `yaml:"Password"`
	DB       int    `yaml:"DB"`
}

type MinioConfig struct {
	Enabled    bool   `yaml:"Enabled"`
	Endpoint   string `yaml:"Endpoint"`
	AccessKey  string `yaml:"AccessKey"`
	SecretKey  string `yaml:"SecretKey"`
	BucketName string `yaml:"BucketName"`
	UseSSL     bool   `yaml:"UseSSL"`
}

type K8sConfig struct {
	Enabled     bool   `yaml:"Enabled"`
	Mode        string `yaml:"Mode"`
	Kubeconfig  string `yaml:"Kubeconfig"`
	Context     string `yaml:"Context"`
	ClusterName string `yaml:"ClusterName"`
	Environment string `yaml:"Environment"`
	ClusterType string `yaml:"ClusterType"`
}
