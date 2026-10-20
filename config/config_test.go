package config

import (
	"testing"
	"time"
)

func TestApplyEnvOverridesSMTP(t *testing.T) {
	t.Setenv("SMTP_ENABLED", "true")
	t.Setenv("SMTP_HOST", "smtp.qq.com")
	t.Setenv("SMTP_PORT", "465")
	t.Setenv("SMTP_USERNAME", "sender@qq.com")
	t.Setenv("SMTP_PASSWORD", "secret-from-env")
	t.Setenv("SMTP_FROM", "sender@qq.com")
	t.Setenv("SMTP_FROM_NAME", "代码仓库分析工作台")
	t.Setenv("SMTP_TLS", "ssl")
	t.Setenv("SMTP_TIMEOUT_SECONDS", "12")
	t.Setenv("AUTH_REGISTER_ENABLED", "true")

	conf := &Config{}
	applyEnvOverrides(conf)

	if !conf.Auth.SMTP.Enabled || !conf.Auth.Register.Enabled {
		t.Fatal("expected smtp and register to be enabled")
	}
	if conf.Auth.SMTP.Host != "smtp.qq.com" || conf.Auth.SMTP.Port != 465 {
		t.Fatalf("unexpected smtp host/port: %+v", conf.Auth.SMTP)
	}
	if conf.Auth.SMTP.Password != "secret-from-env" {
		t.Fatal("SMTP_PASSWORD should be injected from env")
	}
	if conf.Auth.SMTP.TimeoutSeconds != 12 {
		t.Fatalf("timeout = %d", conf.Auth.SMTP.TimeoutSeconds)
	}
	if !conf.Auth.EmailRegisterEnabled() {
		t.Fatal("expected email register to be enabled")
	}
}

// 空字符串不覆盖 YAML 值；SMTP_PASSWORD 例外，允许显式置空。
func TestApplyEnvOverridesIgnoresEmptyValues(t *testing.T) {
	t.Setenv("SMTP_HOST", "")
	t.Setenv("SMTP_PASSWORD", "")
	conf := &Config{}
	conf.Auth.SMTP.Host = "smtp.example.com"
	conf.Auth.SMTP.Password = "from-yaml"
	applyEnvOverrides(conf)
	if conf.Auth.SMTP.Host != "smtp.example.com" {
		t.Fatalf("empty env should not override host: %q", conf.Auth.SMTP.Host)
	}
	if conf.Auth.SMTP.Password != "" {
		t.Fatalf("SMTP_PASSWORD empty env should clear password, got %q", conf.Auth.SMTP.Password)
	}
}

func TestSMTPEffectiveValues(t *testing.T) {
	ssl := SMTP{}
	if ssl.EffectiveTLS() != "ssl" || ssl.EffectivePort() != 465 {
		t.Fatalf("defaults should be ssl:465, got %s:%d", ssl.EffectiveTLS(), ssl.EffectivePort())
	}
	starttls := SMTP{TLS: "starttls"}
	if starttls.EffectivePort() != 587 {
		t.Fatalf("starttls default port = %d", starttls.EffectivePort())
	}
	if got := (SMTP{TimeoutSeconds: 3}).EffectiveTimeout(); got != 3*time.Second {
		t.Fatalf("timeout = %v", got)
	}
	if got := (SMTP{TimeoutSeconds: -1}).EffectiveTimeout(); got != 10*time.Second {
		t.Fatalf("fallback timeout = %v", got)
	}
}

func TestRegisterEffectiveValues(t *testing.T) {
	reg := Register{}
	if reg.EffectiveCodeTTL() != 10*time.Minute {
		t.Fatalf("code ttl = %v", reg.EffectiveCodeTTL())
	}
	if reg.EffectiveResendInterval() != 60*time.Second {
		t.Fatalf("resend = %v", reg.EffectiveResendInterval())
	}
	if reg.EffectiveMaxAttempts() != 5 {
		t.Fatalf("attempts = %d", reg.EffectiveMaxAttempts())
	}
	if reg.EffectivePlan() != "free" {
		t.Fatalf("plan = %q", reg.EffectivePlan())
	}
}

func TestEmailRegisterRequiresSMTP(t *testing.T) {
	conf := Auth{Register: Register{Enabled: true}}
	if conf.EmailRegisterEnabled() {
		t.Fatal("register should require valid SMTP config")
	}
	conf.SMTP = SMTP{Enabled: true, Host: "smtp.qq.com", From: "a@qq.com", Username: "a@qq.com"}
	if conf.EmailRegisterEnabled() {
		t.Fatal("username without password should be treated as invalid SMTP config")
	}
	conf.SMTP.Password = "secret"
	if !conf.EmailRegisterEnabled() {
		t.Fatal("expected register to be enabled")
	}
}
