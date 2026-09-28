package adaptor

import (
	"context"
	"edu.agent.code/adaptor/repo/model"
	"edu.agent.code/config"
	"fmt"
	"github.com/milvus-io/milvus/client/v2/milvusclient"
	"github.com/redis/go-redis/v9"

	// 下面这个驱动可能有问题 如果报错换成 "github.com/glebarez/sqlite"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/plugin/opentelemetry/tracing"
	"os"
	"path/filepath"
	"time"
)

type IAdaptor interface {
	GetConfig() *config.Config
	GetDB() *gorm.DB
	GetMilvusClient() *milvusclient.Client
	GetRedis() *redis.Client
}

type Adaptor struct {
	conf         *config.Config
	db           *gorm.DB
	milvusClient *milvusclient.Client
	redisClient  *redis.Client
}

func NewAdaptor(conf *config.Config) (IAdaptor, error) {
	adaptor := &Adaptor{conf: conf}
	err := adaptor.openDB(conf.SQLite.Path)
	if err != nil {
		return nil, err
	}
	if err := adaptor.openRedisClient(); err != nil {
		return nil, err
	}
	if conf.RAG.Enabled {
		if err := adaptor.openMilvusClient(); err != nil {
			return nil, err
		}
	}
	return adaptor, nil
}

func (a *Adaptor) openDB(path string) error {
	if path == "" {
		return fmt.Errorf("path can't be empty")
	}
	dir := filepath.Dir(path)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("oepn db dir %s: %v", dir, err.Error())
		}
	}
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return fmt.Errorf("oepn sqlite: %v", err)
	}
	err = db.Use(tracing.NewPlugin())
	if err != nil {
		return fmt.Errorf("use plugin: %v", err)
	}
	err = db.AutoMigrate(
		&model.Profile{},
		&model.Session{},
		&model.ChatMessage{},
		&model.CostRecord{},
		&model.QuotaUsage{},
		&model.Approval{},
		&model.AgentCheckpoint{},
	)
	if err != nil {
		return fmt.Errorf("auto migrate: %v", err)
	}
	a.db = db
	return nil
}

func (adaptor *Adaptor) GetConfig() *config.Config {
	return adaptor.conf
}

func (adaptor *Adaptor) GetDB() *gorm.DB {
	return adaptor.db
}

func (a *Adaptor) GetMilvusClient() *milvusclient.Client {
	if !a.conf.RAG.Enabled {
		return nil
	}
	return a.milvusClient
}

// GetRedis 返回登录令牌存储使用的 Redis 客户端。
func (a *Adaptor) GetRedis() *redis.Client {
	return a.redisClient
}

// openRedisClient 建立 Redis 连接并探活。登录令牌依赖 Redis，连接失败直接启动失败，
// 避免服务起来后所有请求都在鉴权阶段报错。
func (a *Adaptor) openRedisClient() error {
	if a.redisClient != nil {
		return nil
	}
	conf := a.conf.Redis
	if conf.Addr == "" {
		return fmt.Errorf("redis addr can't be empty")
	}
	client := redis.NewClient(&redis.Options{
		Addr:     conf.Addr,
		Username: conf.Username,
		Password: conf.Password,
		DB:       conf.DB,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return fmt.Errorf("redis ping %s: %v", conf.Addr, err)
	}
	a.redisClient = client
	return nil
}

func (a *Adaptor) openMilvusClient() error {
	if a.milvusClient != nil {
		return nil
	}
	cli, err := milvusclient.New(context.Background(), &milvusclient.ClientConfig{
		Address:  a.conf.RAG.Milvus.Address,
		Username: a.conf.RAG.Milvus.UserName,
		Password: a.conf.RAG.Milvus.Password,
	})
	if err != nil {
		return fmt.Errorf("Adaptor create milvus client: %v", err)
	}
	a.milvusClient = cli
	return nil
}
