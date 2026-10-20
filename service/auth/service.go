// Package auth 提供账号密码登录、邮箱验证码注册与登录令牌的签发、校验、吊销。
//
// 配置账号固定在 agent_code_local.yml 的 auth.accounts 中；邮箱注册用户存放在
// SQLite 的 users 表；登录令牌统一存放于 Redis，因此服务重启不会让已登录用户掉线，
// 登出也能即时吊销。邮箱验证码同样只以哈希形式短时存放在 Redis，不落库、不写日志。
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"edu.agent.code/adaptor"
	"edu.agent.code/adaptor/repo/user"
	"edu.agent.code/common"
	"edu.agent.code/config"
	"edu.agent.code/service/do"
	"edu.agent.code/utils/logger"
	"edu.agent.code/utils/mailer"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/mail"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
)

// ErrInvalidCredentials 表示账号或密码错误。邮箱不存在与密码错误返回同一错误，
// 避免通过错误信息区分出哪些账号真实存在。
var ErrInvalidCredentials = errors.New("invalid credentials")

// ErrUnauthorized 表示令牌缺失、已过期或已被吊销。
var ErrUnauthorized = errors.New("unauthorized")

// ErrGuestDisabled 表示服务端未开启游客登录。
var ErrGuestDisabled = errors.New("guest login disabled")

// 邮箱注册相关错误。
var (
	ErrRegisterDisabled = errors.New("email register disabled")
	ErrInvalidEmail     = errors.New("invalid email")
	ErrEmailRegistered  = errors.New("email already registered")
	ErrSendTooFrequent  = errors.New("send too frequent")
	ErrCodeExpired      = errors.New("verification code expired")
	ErrCodeInvalid      = errors.New("verification code invalid")
	ErrCodeTooMany      = errors.New("verification code attempts exceeded")
	ErrCodeSendFailed   = errors.New("verification code send failed")
	ErrPasswordTooShort = errors.New("password too short")
	ErrPasswordTooLong  = errors.New("password too long")
	ErrPasswordWeak     = errors.New("password has no letter or digit")
	ErrLoginTooFrequent = errors.New("login too frequent")
	ErrUnsupportedScope = errors.New("unsupported code purpose")
)

const (
	// defaultTokenTTLHours 是未配置 auth.token_ttl_hours 时的令牌有效期：7 天。
	defaultTokenTTLHours = 168
	// defaultGuestTokenTTLHours 是未配置 auth.guest.token_ttl_hours 时的游客令牌有效期：24 小时。
	defaultGuestTokenTTLHours = 24
	tokenKeyPrefix            = "auth:token:"
	// guestIDHexLen 是游客身份哈希截取的字符数，用于体验限流时碰撞概率足够低。
	guestIDHexLen = 16

	// 验证码相关 Redis key 前缀。
	codeKeyPrefix     = "auth:email:code:"
	attemptsKeyPrefix = "auth:email:code:attempts:"
	cooldownKeyPrefix = "auth:email:code:cooldown:"
	ipRateKeyPrefix   = "auth:email:code:ip:"
	loginFailPrefix   = "auth:email:login_fail:"

	// emailCodeIPLimit / emailCodeIPWindow 是同一 IP 的验证码发送上限。
	emailCodeIPLimit  = 10
	emailCodeIPWindow = 10 * time.Minute
	// loginFailLimit / loginFailWindow 是同一邮箱+IP 的登录失败上限（仅邮箱账号路径）。
	loginFailLimit  = 10
	loginFailWindow = 15 * time.Minute
	// bcryptCost 与规范要求一致：cost 10。
	bcryptCost = 10
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
	users    user.IUser
	mailer   *mailer.Mailer
	register registerConf
}

// guestConf 是游客登录的运行时配置，取自 auth.guest。
type guestConf struct {
	enabled  bool
	plan     common.Plan
	tokenTTL time.Duration
}

// registerConf 是邮箱注册的运行时配置，来自 auth.register / auth.smtp。
type registerConf struct {
	enabled     bool
	plan        common.Plan
	codeTTL     time.Duration
	resend      time.Duration
	maxAttempts int
}

func NewService(adaptor adaptor.IAdaptor) *Service {
	conf := adaptor.GetConfig().Auth
	var mail *mailer.Mailer
	if conf.SMTP.Enabled {
		mail = mailer.New(mailer.Config{
			Host:     conf.SMTP.Host,
			Port:     conf.SMTP.EffectivePort(),
			Username: conf.SMTP.Username,
			Password: conf.SMTP.Password,
			From:     conf.SMTP.From,
			FromName: conf.SMTP.FromName,
			TLS:      conf.SMTP.EffectiveTLS(),
			Timeout:  conf.SMTP.EffectiveTimeout(),
		})
	}
	return &Service{
		redis:    adaptor.GetRedis(),
		tokenTTL: tokenTTL(conf.TokenTTLHours),
		accounts: buildAccounts(conf.Accounts),
		guest:    buildGuest(conf),
		users:    user.NewUser(adaptor),
		mailer:   mail,
		register: buildRegister(conf),
	}
}

// RegisterEnabled 报告邮箱注册是否可用：配置开关打开且 SMTP 配置有效。
func (s *Service) RegisterEnabled() bool {
	return s.register.enabled
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

// SendRegisterCode 生成并发送注册验证码。
//
// 返回验证码有效期与重发冷却时间，供前端展示倒计时。验证码只把
// SHA-256(email|purpose|code) 写进 Redis；发送失败时清理本次验证码与冷却，
// 让用户可以立即重试。
func (s *Service) SendRegisterCode(ctx context.Context, rawEmail, purpose, ip string) (time.Duration, time.Duration, error) {
	if !s.register.enabled {
		return 0, 0, ErrRegisterDisabled
	}
	purpose = strings.ToLower(strings.TrimSpace(purpose))
	if purpose == "" {
		purpose = "register"
	}
	if purpose != "register" {
		return 0, 0, ErrUnsupportedScope
	}
	address, err := NormalizeEmail(rawEmail)
	if err != nil {
		return 0, 0, err
	}
	existing, err := s.users.GetByEmail(ctx, address)
	if err != nil {
		logger.Error("auth: query user by email failed, err=%v", err)
		return 0, 0, err
	}
	if existing != nil {
		return 0, 0, ErrEmailRegistered
	}

	if err := s.checkIPRate(ctx, purpose, ip); err != nil {
		return 0, 0, err
	}
	cooldownKey := cooldownKeyPrefix + purpose + ":" + address
	ok, err := s.redis.SetNX(ctx, cooldownKey, "1", s.register.resend).Result()
	if err != nil {
		logger.Error("auth: set email code cooldown failed, err=%v", err)
		return 0, 0, err
	}
	if !ok {
		return 0, 0, ErrSendTooFrequent
	}

	code, err := generateCode()
	if err != nil {
		_ = s.redis.Del(ctx, cooldownKey).Err()
		return 0, 0, err
	}
	codeKey := codeKeyPrefix + purpose + ":" + address
	attemptsKey := attemptsKeyPrefix + purpose + ":" + address
	if err := s.redis.Set(ctx, codeKey, hashCode(address, purpose, code), s.register.codeTTL).Err(); err != nil {
		_ = s.redis.Del(ctx, cooldownKey).Err()
		logger.Error("auth: save email code failed, err=%v", err)
		return 0, 0, err
	}
	_ = s.redis.Del(ctx, attemptsKey).Err()

	if err := s.sendRegisterCodeMail(ctx, address, code); err != nil {
		// 发送失败时回收本次验证码与冷却，避免用户被"发送失败"锁 60 秒。
		_ = s.redis.Del(ctx, codeKey, attemptsKey, cooldownKey).Err()
		logger.Error("auth: send register code failed, email=%s, err=%v", maskEmail(address), err)
		return 0, 0, ErrCodeSendFailed
	}
	logger.Info("auth: register code sent, email=%s", maskEmail(address))
	return s.register.codeTTL, s.register.resend, nil
}

// Register 校验验证码、创建邮箱用户并直接签发登录令牌。
func (s *Service) Register(ctx context.Context, rawEmail, code, password string) (string, *common.UserInfo, error) {
	if !s.register.enabled {
		return "", nil, ErrRegisterDisabled
	}
	address, err := NormalizeEmail(rawEmail)
	if err != nil {
		return "", nil, err
	}
	if err := ValidatePassword(password); err != nil {
		return "", nil, err
	}
	if err := s.consumeRegisterCode(ctx, address, strings.TrimSpace(code)); err != nil {
		return "", nil, err
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		logger.Error("auth: bcrypt password failed, err=%v", err)
		return "", nil, err
	}
	now := time.Now()
	record := &do.User{
		Email:        address,
		PasswordHash: string(hash),
		Plan:         string(s.register.plan),
		LastLoginAt:  &now,
	}
	if err := s.users.Create(ctx, record); err != nil {
		if user.IsUniqueViolation(err) {
			return "", nil, ErrEmailRegistered
		}
		logger.Error("auth: create user failed, err=%v", err)
		return "", nil, err
	}

	info := &common.UserInfo{UserID: address, Plan: s.register.plan}
	token, err := s.issueToken(ctx, info, s.tokenTTL)
	if err != nil {
		return "", nil, err
	}
	logger.Info("auth: user registered, user=%s, plan=%s", maskEmail(address), info.Plan)
	return token, info, nil
}

// Login 校验账号密码并签发令牌。保留原签名供现有调用方与测试使用；
// HTTP 层应使用 LoginWithIP，以启用邮箱账号的失败限流。
func (s *Service) Login(ctx context.Context, identifier, password string) (string, *common.UserInfo, error) {
	return s.LoginWithIP(ctx, identifier, password, "")
}

// LoginWithIP 校验登录凭据并签发令牌。
//
// identifier 可以是配置账号用户名，也可以是注册邮箱。邮箱路径先查 users 表并
// 用 bcrypt 校验；未命中时回落配置账号，保证 Anyeling / visitor 等老账号不受影响。
// 两条路径失败时的对外错误完全一致，不暴露邮箱是否存在。
func (s *Service) LoginWithIP(ctx context.Context, identifier, password, ip string) (string, *common.UserInfo, error) {
	identifier = strings.TrimSpace(identifier)
	if identifier == "" || password == "" {
		return "", nil, ErrInvalidCredentials
	}

	email, emailErr := NormalizeEmail(identifier)
	if emailErr == nil {
		if s.tooManyLoginFailures(ctx, email, ip) {
			return "", nil, ErrLoginTooFrequent
		}
		record, err := s.users.GetByEmail(ctx, email)
		if err != nil {
			logger.Error("auth: query user for login failed, err=%v", err)
			return "", nil, err
		}
		if record != nil {
			if bcrypt.CompareHashAndPassword([]byte(record.PasswordHash), []byte(password)) != nil {
				s.recordLoginFailure(ctx, email, ip)
				logger.Warn("auth: email login rejected, user=%s", maskEmail(email))
				return "", nil, ErrInvalidCredentials
			}
			s.clearLoginFailures(ctx, email, ip)
			now := time.Now()
			if err := s.users.UpdateLastLogin(ctx, email, now); err != nil {
				logger.Warn("auth: update last login failed, user=%s, err=%v", email, err)
			}
			info := &common.UserInfo{UserID: email, Plan: planOrDefault(record.Plan)}
			token, err := s.issueToken(ctx, info, s.tokenTTL)
			if err != nil {
				return "", nil, err
			}
			return token, info, nil
		}
		// 合法邮箱但 users 表未命中：继续尝试配置账号（兼容以邮箱形式配置的老账号）。
	}

	acc, ok := s.accounts[identifier]
	if !ok || subtle.ConstantTimeCompare([]byte(acc.password), []byte(password)) != 1 {
		if emailErr == nil {
			s.recordLoginFailure(ctx, email, ip)
		}
		logger.Warn("auth: login rejected, user=%s", identifier)
		return "", nil, ErrInvalidCredentials
	}
	token, err := s.issueToken(ctx, acc.user, s.tokenTTL)
	if err != nil {
		return "", nil, err
	}
	return token, acc.user, nil
}

// issueToken 把用户信息与令牌的映射写入 Redis 并带有效期。游客、配置账号与邮箱用户
// 共用同一条路径，因此三者在鉴权、配额、成本统计上完全等价，只是 UserID 来源不同。
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

// sendRegisterCodeMail 渲染验证码邮件并通过 SMTP 发送。
// 验证码只进入邮件正文，不出现在任何日志或错误信息里。
func (s *Service) sendRegisterCodeMail(ctx context.Context, address, code string) error {
	if s.mailer == nil {
		return errors.New("smtp mailer is not configured")
	}
	minutes := int(s.register.codeTTL / time.Minute)
	if minutes <= 0 {
		minutes = 10
	}
	subject := "【代码仓库分析工作台】邮箱验证码"
	text := fmt.Sprintf(
		"你好：\n\n你正在注册代码仓库分析工作台，本次验证码为：%s\n\n"+
			"验证码 %d 分钟内有效，仅可使用一次，请勿泄露给他人。\n"+
			"如果这不是你的操作，请忽略本邮件。\n",
		code, minutes,
	)
	html := fmt.Sprintf(`<!doctype html>
<html lang="zh-CN"><body style="margin:0;padding:24px;background:#f4f1e6;color:#1b2024;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',sans-serif;">
<div style="max-width:460px;margin:0 auto;padding:32px 28px;background:#f8f5ec;border:1px solid rgba(27,32,36,0.32);border-radius:2px;">
<p style="margin:0 0 8px;font-size:16px;font-weight:700;">代码仓库分析工作台</p>
<p style="margin:0 0 20px;color:#54584f;font-size:14px;line-height:1.6;">你正在注册账号，请在页面中输入以下验证码：</p>
<p style="margin:0 0 20px;font-family:Menlo,Consolas,monospace;font-size:32px;font-weight:700;letter-spacing:8px;color:#2a6e6a;">%s</p>
<p style="margin:0;color:#63675c;font-size:12px;line-height:1.7;">验证码 %d 分钟内有效，仅可使用一次。<br>如果这不是你的操作，请忽略本邮件。</p>
</div>
</body></html>`, code, minutes)
	return s.mailer.Send(ctx, address, subject, text, html)
}

// consumeRegisterCode 校验并一次性消费注册验证码。
// 错误次数达到上限后立即删除验证码，必须重新获取。
func (s *Service) consumeRegisterCode(ctx context.Context, address, code string) error {
	codeKey := codeKeyPrefix + "register:" + address
	attemptsKey := attemptsKeyPrefix + "register:" + address
	expected, err := s.redis.Get(ctx, codeKey).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return ErrCodeExpired
		}
		logger.Error("auth: read email code failed, err=%v", err)
		return err
	}

	actual := hashCode(address, "register", code)
	if subtle.ConstantTimeCompare([]byte(expected), []byte(actual)) != 1 {
		attempts, incrErr := s.redis.Incr(ctx, attemptsKey).Result()
		if incrErr != nil {
			logger.Error("auth: increase code attempts failed, err=%v", incrErr)
		} else {
			_ = s.redis.Expire(ctx, attemptsKey, s.register.codeTTL).Err()
			if int(attempts) >= s.register.maxAttempts {
				_ = s.redis.Del(ctx, codeKey, attemptsKey).Err()
				return ErrCodeTooMany
			}
		}
		return ErrCodeInvalid
	}

	_ = s.redis.Del(ctx, codeKey, attemptsKey).Err()
	return nil
}

// checkIPRate 对同一 IP 的验证码发送做 10 分钟滑动窗口限流。
func (s *Service) checkIPRate(ctx context.Context, purpose, ip string) error {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return nil
	}
	key := ipRateKeyPrefix + purpose + ":" + ip
	count, err := s.redis.Incr(ctx, key).Result()
	if err != nil {
		logger.Error("auth: increase email code ip rate failed, err=%v", err)
		return err
	}
	if count == 1 {
		_ = s.redis.Expire(ctx, key, emailCodeIPWindow).Err()
	}
	if count > emailCodeIPLimit {
		return ErrSendTooFrequent
	}
	return nil
}

func (s *Service) tooManyLoginFailures(ctx context.Context, email, ip string) bool {
	if ip == "" {
		return false
	}
	count, err := s.redis.Get(ctx, loginFailKey(email, ip)).Int64()
	if err != nil {
		if !errors.Is(err, redis.Nil) {
			logger.Warn("auth: read login failure counter failed, err=%v", err)
		}
		return false
	}
	return count >= loginFailLimit
}

// recordLoginFailure 只对邮箱账号路径计数，不影响配置账号。
func (s *Service) recordLoginFailure(ctx context.Context, email, ip string) {
	if ip == "" {
		return
	}
	key := loginFailKey(email, ip)
	count, err := s.redis.Incr(ctx, key).Result()
	if err != nil {
		logger.Warn("auth: increase login failure counter failed, err=%v", err)
		return
	}
	if count == 1 {
		_ = s.redis.Expire(ctx, key, loginFailWindow).Err()
	}
}

func (s *Service) clearLoginFailures(ctx context.Context, email, ip string) {
	if ip == "" {
		return
	}
	_ = s.redis.Del(ctx, loginFailKey(email, ip)).Err()
}

func loginFailKey(email, ip string) string {
	return loginFailPrefix + email + ":" + ip
}

// NormalizeEmail 规范化并校验邮箱：去空格、转小写、长度 <=254，
// 必须是裸地址（不允许显示名），域名需包含点号。
func NormalizeEmail(raw string) (string, error) {
	email := strings.ToLower(strings.TrimSpace(raw))
	if email == "" || len(email) > 254 || strings.ContainsAny(email, " \t\r\n") {
		return "", ErrInvalidEmail
	}
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Name != "" || addr.Address != email {
		return "", ErrInvalidEmail
	}
	at := strings.LastIndex(email, "@")
	if at <= 0 || at == len(email)-1 || at > 64 {
		return "", ErrInvalidEmail
	}
	domain := email[at+1:]
	if !strings.Contains(domain, ".") ||
		strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") ||
		strings.Contains(domain, "..") {
		return "", ErrInvalidEmail
	}
	return email, nil
}

// ValidatePassword 校验注册密码：8–72 字节，且同时包含 ASCII 字母和数字。
// 72 字节是 bcrypt 的输入上限；超长直接拒绝而不是被静默截断。
func ValidatePassword(password string) error {
	if len(password) < 8 {
		return ErrPasswordTooShort
	}
	if len(password) > 72 {
		return ErrPasswordTooLong
	}
	hasLetter, hasDigit := false, false
	for i := 0; i < len(password); i++ {
		ch := password[i]
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z':
			hasLetter = true
		case ch >= '0' && ch <= '9':
			hasDigit = true
		}
	}
	if !hasLetter || !hasDigit {
		return ErrPasswordWeak
	}
	return nil
}

// hashCode 是验证码在 Redis 中的存储形式：SHA-256(email|purpose|code)。
// 它避免验证码以明文出现在 Redis 快照或调试输出里；但 6 位数字空间很小，
// 哈希不承担抵御离线爆破的职责，真正的防护是短 TTL、一次性消费与尝试次数限制。
func hashCode(email, purpose, code string) string {
	sum := sha256.Sum256([]byte(email + "|" + purpose + "|" + code))
	return hex.EncodeToString(sum[:])
}

// generateCode 用 crypto/rand 生成 6 位数字验证码（含前导零）。
func generateCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

// maskEmail 在日志里遮蔽邮箱的本地部分，只保留前两位与域名。
func maskEmail(email string) string {
	at := strings.LastIndex(email, "@")
	if at <= 0 {
		return "***"
	}
	local := email[:at]
	if len(local) > 2 {
		local = local[:2] + "***"
	} else {
		local = "***"
	}
	return local + email[at:]
}

func planOrDefault(raw string) common.Plan {
	plan := common.Plan(strings.TrimSpace(raw))
	if !isKnownPlan(plan) {
		return common.PlanFree
	}
	return plan
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

// buildRegister 解析 auth.register / auth.smtp。注册只有在 SMTP 配置有效时才真正开放。
func buildRegister(conf config.Auth) registerConf {
	plan := common.Plan(conf.Register.EffectivePlan())
	return registerConf{
		enabled:     conf.EmailRegisterEnabled(),
		plan:        plan,
		codeTTL:     conf.Register.EffectiveCodeTTL(),
		resend:      conf.Register.EffectiveResendInterval(),
		maxAttempts: conf.Register.EffectiveMaxAttempts(),
	}
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
