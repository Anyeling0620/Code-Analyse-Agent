// Package auth 提供账号密码登录与登录令牌的签发、校验、吊销。
//
// 账号固定配置在 agent_code_local.yml 的 auth.accounts 中，令牌存放于 Redis，
// 因此服务重启不会让已登录用户掉线，登出也能即时吊销。
package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"edu.agent.code/adaptor"
	"edu.agent.code/common"
	"edu.agent.code/config"
	"edu.agent.code/utils/logger"
	"encoding/hex"
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

// ErrGuestDisabled 表示服务端未开启游客登录。
var ErrGuestDisabled = errors.New("guest login disabled")

const (
	// defaultTokenTTLHours 是未配置 auth.token_ttl_hours 时的令牌有效期：7 天。
	defaultTokenTTLHours = 168
	// defaultGuestTokenTTLHours 是未配置 auth.guest.token_ttl_hours 时的游客令牌有效期：24 小时。
	defaultGuestTokenTTLHours = 24
	tokenKeyPrefix            = "auth:token:"
	// guestIDHexLen 是游客身份哈希截取的字符数，用于体验限流时碰撞概率足够低。
	guestIDHexLen = 16
)

type account struct {
	password string
	user     *common.UserInfo
}

type Service struct {
	redis    *redis.Client
	tokenTTL time.Duration
	accounts map[string]account
	guest    guestConf
}

// guestConf 是游客登录的运行时配置，取自 auth.guest。
type guestConf struct {
	enabled  bool
	plan     common.Plan
	tokenTTL time.Duration
}

func NewService(adaptor adaptor.IAdaptor) *Service {
	conf := adaptor.GetConfig().Auth
	return &Service{
		redis:    adaptor.GetRedis(),
		tokenTTL: tokenTTL(conf.TokenTTLHours),
		accounts: buildAccounts(conf.Accounts),
		guest:    buildGuest(conf),
	}
}

// GuestLogin 签发游客令牌。身份由 IP 与浏览器指纹派生，因此"同一 IP + 同一浏览器"
// 共用一个配额身份：换 IP 或换浏览器即换身份，同一身份重复进入则用量继续累加。
func (s *Service) GuestLogin(ctx context.Context, ip, fingerprint string) (string, *common.UserInfo, error) {
	if !s.guest.enabled {
		return "", nil, ErrGuestDisabled
	}
	user := &common.UserInfo{
		UserID: GuestIdentity(ip, fingerprint),
		Plan:   s.guest.plan,
	}
	token, err := s.issueToken(ctx, user, s.guest.tokenTTL)
	if err != nil {
		return "", nil, err
	}
	logger.Info("auth: guest login, user=%s, plan=%s", user.UserID, user.Plan)
	return token, user, nil
}

// GuestIdentity 由 IP 与浏览器指纹派生稳定的游客身份 ID。
// 指纹来自前端采集的浏览器特征哈希；指纹缺失时退化为只按 IP 识别。
func GuestIdentity(ip, fingerprint string) string {
	seed := strings.TrimSpace(ip) + "|" + strings.TrimSpace(fingerprint)
	sum := sha256.Sum256([]byte(seed))
	return common.GuestUserPrefix + hex.EncodeToString(sum[:])[:guestIDHexLen]
}

// Login 校验账号密码并签发令牌，令牌与用户信息的映射写入 Redis 并带有效期。
func (s *Service) Login(ctx context.Context, username, password string) (string, *common.UserInfo, error) {
	acc, ok := s.accounts[strings.TrimSpace(username)]
	if !ok || subtle.ConstantTimeCompare([]byte(acc.password), []byte(password)) != 1 {
		logger.Warn("auth: login rejected, user=%s", username)
		return "", nil, ErrInvalidCredentials
	}
	token, err := s.issueToken(ctx, acc.user, s.tokenTTL)
	if err != nil {
		return "", nil, err
	}
	return token, acc.user, nil
}

// issueToken 把用户信息与令牌的映射写入 Redis 并带有效期。游客与账号登录共用同一条路径，
// 因此两者在鉴权、配额、成本统计上完全等价，只是 UserID 来源不同。
func (s *Service) issueToken(ctx context.Context, user *common.UserInfo, ttl time.Duration) (string, error) {
	payload, err := json.Marshal(user)
	if err != nil {
		return "", err
	}
	token := common.GetUUIDHex()
	if err := s.redis.Set(ctx, tokenKeyPrefix+token, payload, ttl).Err(); err != nil {
		logger.Error("auth: save token failed, user=%s, err=%v", user.UserID, err)
		return "", err
	}
	return token, nil
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

// buildGuest 解析 auth.guest：套餐为空或取值非法时按 plus 处理（游客默认 plus 计划）。
func buildGuest(conf config.Auth) guestConf {
	plan := common.Plan(strings.TrimSpace(conf.Guest.Plan))
	if plan == "" {
		plan = common.PlanPlus
	}
	if !isKnownPlan(plan) {
		logger.Warn("auth: guest has unknown plan=%s, falls back to PlanPlus", conf.Guest.Plan)
		plan = common.PlanPlus
	}
	return guestConf{
		enabled:  conf.Guest.Enabled,
		plan:     plan,
		tokenTTL: guestTokenTTL(conf),
	}
}

// guestTokenTTL 计算游客令牌有效期：默认 24 小时，且不长于账号令牌的有效期。
func guestTokenTTL(conf config.Auth) time.Duration {
	hours := conf.Guest.TokenTTLHours
	if hours <= 0 {
		hours = defaultGuestTokenTTLHours
	}
	ttl := time.Duration(hours) * time.Hour
	if base := tokenTTL(conf.TokenTTLHours); ttl > base {
		return base
	}
	return ttl
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
