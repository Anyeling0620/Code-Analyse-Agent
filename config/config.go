package config

import (
	"flag"
	"fmt"
	"github.com/go-viper/mapstructure/v2"
	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
	"os"
	"time"
)

const (
	ServerName     = "agent_code"
	ServerFullName = "edu.agent.code"
)

var (
	etcdKey         string
	etcdEnv         string
	etcdAddr        string
	localConfigPath string
	GlobalConfig    Config
)

//goland:noinspection SpellCheckingInspection
type Config struct {
	Server     Server     `yaml:"server"`
	SQLite     SQLite     `yaml:"sqlite"`
	DeepSeek   DeepSeek   `yaml:"deepseek"`
	OTel       OTel       `yaml:"otel"`
	Agents     Agents     `yaml:"agents"`
	ModelPrice ModelPrice `yaml:"model_price"`
}

type Server struct {
	AppName  string `yaml:"app_name"`
	Version  string `yaml:"version"`
	HTTPAddr string `yaml:"http_addr"`
	LogLevel string `yaml:"log_level"`
}

type SQLite struct {
	Path string `yaml:"path"`
}

type DeepSeek struct {
	APIKey  string `yaml:"api_key"`
	BaseURL string `yaml:"base_url"`
	Model   string `yaml:"model"`
}

type OTel struct {
	Endpoint   string  `yaml:"endpoint"`
	SampleRate float64 `yaml:"sample_rate"`
	StdOut     bool    `yaml:"std_out"`
}

type ModelPrice struct {
	FallbackModel string                   `yaml:"fallback_model"`
	PriceCNY      map[string]ModelPriceCNY `yaml:"price_cny"`
}

type ModelPriceCNY struct {
	Prompt     float64 `yaml:"prompt"`
	Completion float64 `yaml:"completion"`
}

type Agents struct {
}

func init() {
	flag.StringVar(&localConfigPath, "c", ServerName+"_local.yml", "path to local config file")
	flag.StringVar(&etcdAddr, "r", os.Getenv("ETCD_ADDR"), "ETCD server address")
	flag.StringVar(&etcdEnv, "e", "env", "ETCD environment")
}

func InitConfig() *Config {
	vipConf := viper.New()
	vipConf.SetConfigType("yaml")
	flag.Parse()

	etcdKey = fmt.Sprintf("/config/%s/%s/system", ServerFullName, etcdEnv)
	if etcdAddr != "" {
		conf, err := getFromRemoteAndWatchUpdate(vipConf)
		if err != nil {
			panic(err)
		}
		GlobalConfig = *conf
		return conf
	}
	conf, err := getFromLocal()
	if err != nil {
		panic(err)
	}
	if conf.DeepSeek.APIKey == "" {
		conf.DeepSeek.APIKey = os.Getenv("DEEPSEEK_API_KEY")
	}
	GlobalConfig = *conf
	return conf
}

func getFromLocal() (*Config, error) {
	data, err := os.ReadFile(localConfigPath)
	if err != nil {
		return nil, err
	}
	var config Config
	err = yaml.Unmarshal(data, &config)
	if err != nil {
		return nil, err
	}
	return &config, nil
}

func getFromRemoteAndWatchUpdate(v *viper.Viper) (*Config, error) {
	if err := v.AddRemoteProvider("etcd3", etcdAddr, etcdKey); err != nil {
		return nil, err
	}
	if err := v.ReadRemoteConfig(); err != nil {
		return nil, err
	}
	config := Config{}
	if err := unmarshalViperConfig(v, &config); err != nil {
		return nil, err
	}
	go func() {
		for {
			time.Sleep(time.Minute)
			if err := v.WatchRemoteConfig(); err == nil {
				_ = unmarshalViperConfig(v, &GlobalConfig)
			}
		}
	}()

	return &config, nil
}

func unmarshalViperConfig(v *viper.Viper, config *Config) error {
	return v.Unmarshal(config, func(config *mapstructure.DecoderConfig) {
		config.TagName = "yaml"
	})
}
