// Package auth 提供账号密码登录与登录令牌的签发、校验、吊销。
//
// 账号固定配置在 agent_code_local.yml 的 auth.accounts 中，令牌存放于 Redis，
// 因此服务重启不会让已登录用户掉线，登出也能即时吊销。
package auth

import (
	"context"
	"crypto/subtle"
	"edu.agent.code/adaptor"
	"edu.agent.code/common"
	"edu.agent.code/config"
	"edu.agent.code/utils/logger"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// ErrInvalidCredentials 表示账号或密码错误。用户名不存在与密码错误返回同一错误，
// 避免通过错误信息区分出哪些账号真实存在。
var ErrInvalidCredentials = errors.New("invalid credentials")

// ErrUnauthorized 表示令牌缺失、已过期或已被吊销。
var ErrUnauthorized = errors.New("unauthorized")

const (
	// defaultTokenTTLHours 是未配置 auth.token_ttl_hours 时的令牌有效期：7 天。
	defaultTokenTTLHours = 168
	tokenKeyPrefix       = "auth:token:"
)

type account struct {
	password string
	user     *common.UserInfo
}

type Service struct {
	redis    *redis.Client
	tokenTTL time.Duration
	accounts map[string]account
}

func NewService(adaptor adaptor.IAdaptor) *Service {
	conf := adaptor.GetConfig().Auth
	return &Service{
		redis:    adaptor.GetRedis(),
		tokenTTL: tokenTTL(conf.TokenTTLHours),
		accounts: buildAccounts(conf.Accounts),
	}
}

// Login 校验账号密码并签发令牌，令牌与用户信息的映射写入 Redis 并带有效期。
func (s *Service) Login(ctx context.Context, username, password string) (string, *common.UserInfo, error) {
	acc, ok := s.accounts[strings.TrimSpace(username)]
	if !ok || subtle.ConstantTimeCompare([]byte(acc.password), []byte(password)) != 1 {
		logger.Warn("auth: login rejected, user=%s", username)
		return "", nil, ErrInvalidCredentials
	}
	payload, err := json.Marshal(acc.user)
	if err != nil {
		return "", nil, err
	}
	token := common.GetUUIDHex()
	if err := s.redis.Set(ctx, tokenKeyPrefix+token, payload, s.tokenTTL).Err(); err != nil {
		logger.Error("auth: save token failed, user=%s, err=%v", username, err)
		return "", nil, err
	}
	return token, acc.user, nil
}

// Verify 用令牌换回用户信息，令牌不存在（含过期）时返回 ErrUnauthorized。
func (s *Service) Verify(ctx context.Context, token string) (*common.UserInfo, error) {
	raw, err := s.redis.Get(ctx, tokenKeyPrefix+token).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, ErrUnauthorized
		}
		logger.Error("auth: read token failed, err=%v", err)
		return nil, err
	}
	var user common.UserInfo
	if err := json.Unmarshal(raw, &user); err != nil {
		logger.Error("auth: decode token payload failed, err=%v", err)
		return nil, err
	}
	return &user, nil
}

// Logout 吊销令牌。
func (s *Service) Logout(ctx context.Context, token string) error {
	return s.redis.Del(ctx, tokenKeyPrefix+token).Err()
}

func buildAccounts(rows []config.AuthAccount) map[string]account {
	accounts := make(map[string]account, len(rows))
	for _, row := range rows {
		username := strings.TrimSpace(row.Username)
		if username == "" {
			logger.Warn("auth: skip account with empty username")
			continue
		}
		plan := common.Plan(strings.TrimSpace(row.Plan))
		if !isKnownPlan(plan) {
			logger.Warn("auth: account %s has unknown plan=%s, quota falls back to PlanFree", username, row.Plan)
		}
		accounts[username] = account{
			password: row.Password,
			user: &common.UserInfo{
				UserID: username,
				Plan:   plan,
			},
		}
	}
	return accounts
}

func isKnownPlan(plan common.Plan) bool {
	switch plan {
	case common.PlanFree, common.PlanPlus, common.PlanPro:
		return true
	default:
		return false
	}
}

func tokenTTL(hours int) time.Duration {
	if hours <= 0 {
		hours = defaultTokenTTLHours
	}
	return time.Duration(hours) * time.Hour
}
