package adaptor

import (
	"context"
	"edu.agent.code/adaptor/repo/model"
	"edu.agent.code/config"
	applog "edu.agent.code/utils/logger"
	"encoding/json"
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
	"strings"
	"sync"
	"time"
)

const (
	// defaultMilvusDialTimeoutSec 是建立 Milvus 连接的单次超时。
	//
	// milvusclient.New 内部是 grpc.DialContext + grpc.WithBlock：不设超时的话，Milvus 不在线
	// 时它会一直等下去，main 就卡在这一行，HTTP 端口永远不监听。外部表现是「进程活着、
	// 端口没人听、nginx 502」，最容易被误判成"代码没更新/服务没起"。所以拨号必须有上限。
	defaultMilvusDialTimeoutSec = 10
	// maxMilvusDialTimeoutSec 给配置里的 timeout_sec 兜底，避免把它调大后又把启动卡住。
	maxMilvusDialTimeoutSec = 30
	// milvusRetryInitialDelay / milvusRetryMaxDelay 是后台重连的退避区间。
	milvusRetryInitialDelay = 5 * time.Second
	milvusRetryMaxDelay     = 60 * time.Second
)

type IAdaptor interface {
	GetConfig() *config.Config
	GetDB() *gorm.DB
	GetMilvusClient() *milvusclient.Client
	GetRedis() *redis.Client
}

type Adaptor struct {
	conf        *config.Config
	db          *gorm.DB
	redisClient *redis.Client

	// milvusMu 保护 milvusClient / milvusErr：客户端会由后台重连 goroutine 写入，
	// 请求侧随时在读，不能裸改。
	milvusMu        sync.RWMutex
	milvusClient    *milvusclient.Client
	milvusErr       error
	milvusRetryOnce sync.Once
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
		// Milvus 连不上不再让整个服务启动失败：Milvus 常常比本服务晚就绪（开机时 docker 才刚
		// 拉起容器），这里记下降级状态并交给后台重连，HTTP 服务照常起、其余功能不受影响。
		if err := adaptor.openMilvusClient(context.Background()); err != nil {
			applog.Warn("milvus 未就绪，服务以降级模式启动（HTTP 与其余功能不受影响）：%v", err)
			adaptor.startMilvusRetry()
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
		&model.SessionShare{},
		&model.ChatRun{},
		&model.ChatRunEvent{},
	)
	if err != nil {
		return fmt.Errorf("auto migrate: %v", err)
	}
	// 画像字段由 purchased_courses（JSON 数组）换成 description（自由文本）：AutoMigrate
	// 只会新增列，历史数据要靠这里回填一次。迁移失败不拦启动——新列已经建好，只是旧画像
	// 还没搬过来；下次启动会再试一次。
	if err := migrateProfileDescription(db); err != nil {
		applog.Warn("回填用户画像描述失败（不影响启动）：%v", err)
	}
	a.db = db
	return nil
}

// migrateProfileDescription 把旧的 profiles.purchased_courses（JSON 数组）回填到 description。
//
// 幂等：只处理「description 为空且旧列有值」的行；旧列不存在（全新库）或已回填过就直接返回。
// 只回填、不删旧列：旧数据留在原列里，需要回退时把模型改回去就能读回来。
func migrateProfileDescription(db *gorm.DB) error {
	if !db.Migrator().HasColumn("profiles", "purchased_courses") {
		return nil
	}
	type legacyProfile struct {
		UserID           string
		PurchasedCourses string
	}
	var rows []legacyProfile
	err := db.Raw(`SELECT user_id, purchased_courses FROM profiles
		WHERE purchased_courses IS NOT NULL AND purchased_courses <> '' AND purchased_courses <> '[]'
		  AND (description IS NULL OR description = '')`).Scan(&rows).Error
	if err != nil {
		return fmt.Errorf("查询历史画像: %w", err)
	}
	for _, row := range rows {
		description := joinLegacyCourses(row.PurchasedCourses)
		if description == "" {
			continue
		}
		if err := db.Exec(`UPDATE profiles SET description = ? WHERE user_id = ?`, description, row.UserID).Error; err != nil {
			return fmt.Errorf("回填 user_id=%s: %w", row.UserID, err)
		}
		applog.Info("已把历史已购课程回填为描述 user_id=%s", row.UserID)
	}
	return nil
}

// joinLegacyCourses 把旧列里的 JSON 数组文本拼成一段描述；不是合法 JSON 时按原文本返回，
// 避免一条脏数据让整次迁移失败。
func joinLegacyCourses(raw string) string {
	var courses []string
	if err := json.Unmarshal([]byte(raw), &courses); err != nil {
		return strings.TrimSpace(raw)
	}
	kept := make([]string, 0, len(courses))
	for _, course := range courses {
		if trimmed := strings.TrimSpace(course); trimmed != "" {
			kept = append(kept, trimmed)
		}
	}
	return strings.Join(kept, "、")
}

func (adaptor *Adaptor) GetConfig() *config.Config {
	return adaptor.conf
}

func (adaptor *Adaptor) GetDB() *gorm.DB {
	return adaptor.db
}

// GetMilvusClient 返回当前的 Milvus 客户端，Milvus 不可用时返回 nil。
//
// 调用方必须判空（历史调用点本来就是 nil 判定，语义保持不变）。注意它只代表"这一刻连上了"，
// 不代表启动时一定就绪——服务允许在 Milvus 缺位的情况下先起来。
func (a *Adaptor) GetMilvusClient() *milvusclient.Client {
	if a == nil || a.conf == nil || !a.conf.RAG.Enabled {
		return nil
	}
	a.milvusMu.RLock()
	defer a.milvusMu.RUnlock()
	return a.milvusClient
}

// MilvusStatus 报告向量库是否已连上；未连上时 err 是最近一次连接失败的原因。
// 用于健康检查与排障：Milvus 缺位不该是"只有翻日志才能发现"的静默故障。
func (a *Adaptor) MilvusStatus() (bool, error) {
	if a == nil || a.conf == nil || !a.conf.RAG.Enabled {
		return true, nil
	}
	a.milvusMu.RLock()
	defer a.milvusMu.RUnlock()
	return a.milvusClient != nil, a.milvusErr
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

// milvusDialTimeout 取配置 milvus.timeout_sec，并夹在 [默认值, 上限] 之间。
func (a *Adaptor) milvusDialTimeout() time.Duration {
	sec := a.conf.RAG.Milvus.TimeoutSec
	if sec <= 0 {
		sec = defaultMilvusDialTimeoutSec
	}
	if sec > maxMilvusDialTimeoutSec {
		sec = maxMilvusDialTimeoutSec
	}
	return time.Duration(sec) * time.Second
}

// openMilvusClient 建立 Milvus 客户端（阻塞拨号 + 超时）。失败只返回错误，不再由调用方
// 终止进程；成功后由 startMilvusRetry 起的后台 goroutine 收尾。
func (a *Adaptor) openMilvusClient(ctx context.Context) error {
	a.milvusMu.RLock()
	connected := a.milvusClient != nil
	a.milvusMu.RUnlock()
	if connected {
		return nil
	}
	addr := a.conf.RAG.Milvus.Address
	if addr == "" {
		return fmt.Errorf("milvus address can't be empty")
	}

	timeout := a.milvusDialTimeout()
	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := time.Now()
	cli, err := milvusclient.New(dialCtx, &milvusclient.ClientConfig{
		Address:  addr,
		Username: a.conf.RAG.Milvus.UserName,
		Password: a.conf.RAG.Milvus.Password,
	})
	if err != nil {
		// 拨号失败时客户端内部可能已经建过底层连接，避免泄漏。
		if cli != nil {
			_ = cli.Close(context.Background())
		}
		wrapped := fmt.Errorf("milvus connect %s 失败（超时 %s，耗时 %s）: %w",
			addr, timeout, time.Since(start).Round(time.Millisecond), err)
		a.milvusMu.Lock()
		a.milvusErr = wrapped
		a.milvusMu.Unlock()
		return wrapped
	}
	a.milvusMu.Lock()
	a.milvusClient = cli
	a.milvusErr = nil
	a.milvusMu.Unlock()
	applog.Info("milvus connected addr=%s elapsed=%s", addr, time.Since(start).Round(time.Millisecond))
	return nil
}

// startMilvusRetry 在后台重试连接 Milvus：Milvus 晚于本服务启动、或中途重启时，RAG 能力
// 无需重启进程即可恢复。只允许一个重试 goroutine（sync.Once 兜住）。
func (a *Adaptor) startMilvusRetry() {
	a.milvusRetryOnce.Do(func() {
		go func() {
			delay := milvusRetryInitialDelay
			for {
				time.Sleep(delay)
				if err := a.openMilvusClient(context.Background()); err == nil {
					applog.Info("milvus 后台重连成功，RAG 能力已恢复")
					return
				}
				next := delay * 2
				if next > milvusRetryMaxDelay {
					next = milvusRetryMaxDelay
				}
				applog.Warn("milvus 后台重连失败，%s 后重试", next)
				delay = next
			}
		}()
	})
}
