package auth

import (
	"regexp"
	"testing"

	"edu.agent.code/common"
	"edu.agent.code/config"
)

func TestNormalizeEmail(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
		ok    bool
	}{
		{name: "lowercases and trims", input: "  Student@Example.COM ", want: "student@example.com", ok: true},
		{name: "common address", input: "a.b+tag@sub.example.cn", want: "a.b+tag@sub.example.cn", ok: true},
		{name: "empty", input: "   ", ok: false},
		{name: "missing at", input: "student.example.com", ok: false},
		{name: "missing domain dot", input: "student@example", ok: false},
		{name: "display name", input: "Student <student@example.com>", ok: false},
		{name: "space inside", input: "stu dent@example.com", ok: false},
		{name: "too long", input: string(make([]byte, 250)) + "@example.com", ok: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeEmail(tc.input)
			if tc.ok {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if got != tc.want {
					t.Fatalf("got %q, want %q", got, tc.want)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error, got %q", got)
			}
		})
	}
}

func TestValidatePassword(t *testing.T) {
	cases := []struct {
		name     string
		password string
		wantErr  error
	}{
		{name: "valid", password: "abc12345"},
		{name: "valid mixed case", password: "Abc12345!@#"},
		{name: "too short", password: "a1b2c3", wantErr: ErrPasswordTooShort},
		{name: "too long", password: "a1" + string(make([]byte, 71)), wantErr: ErrPasswordTooLong},
		{name: "digits only", password: "12345678", wantErr: ErrPasswordWeak},
		{name: "letters only", password: "abcdefgh", wantErr: ErrPasswordWeak},
		{name: "chinese without ascii letter", password: "密码密码12345678", wantErr: ErrPasswordWeak},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePassword(tc.password)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err != tc.wantErr {
				t.Fatalf("got %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestHashCodeStableAndScoped(t *testing.T) {
	base := hashCode("student@example.com", "register", "123456")
	if len(base) != 64 {
		t.Fatalf("expected sha256 hex length 64, got %d", len(base))
	}
	if again := hashCode("student@example.com", "register", "123456"); again != base {
		t.Fatal("hashCode should be deterministic")
	}
	if other := hashCode("student@example.com", "register", "123457"); other == base {
		t.Fatal("different codes must have different hashes")
	}
	if other := hashCode("student@example.com", "reset", "123456"); other == base {
		t.Fatal("different purposes must have different hashes")
	}
}

func TestGenerateCodeIsSixDigits(t *testing.T) {
	pattern := regexp.MustCompile(`^\d{6}$`)
	for i := 0; i < 50; i++ {
		code, err := generateCode()
		if err != nil {
			t.Fatalf("generateCode: %v", err)
		}
		if !pattern.MatchString(code) {
			t.Fatalf("code %q is not six digits", code)
		}
	}
}

func TestMaskEmail(t *testing.T) {
	if got := maskEmail("student@example.com"); got != "st***@example.com" {
		t.Fatalf("got %q", got)
	}
	if got := maskEmail("a@example.com"); got != "***@example.com" {
		t.Fatalf("got %q", got)
	}
	if got := maskEmail("not-an-email"); got != "***" {
		t.Fatalf("got %q", got)
	}
}

func TestBuildRegisterRequiresValidSMTP(t *testing.T) {
	conf := config.Auth{
		Register: config.Register{Enabled: true},
		SMTP: config.SMTP{
			Enabled: true,
			Host:    "smtp.example.com",
			From:    "noreply@example.com",
			TLS:     "ssl",
		},
	}
	if !buildRegister(conf).enabled {
		t.Fatal("expected register to be enabled with valid SMTP")
	}
	if buildRegister(conf).plan != common.PlanFree {
		t.Fatalf("default plan should be free")
	}

	conf.SMTP.Host = ""
	if buildRegister(conf).enabled {
		t.Fatal("register must be disabled without SMTP host")
	}

	conf.SMTP.Host = "smtp.example.com"
	conf.Register.Plan = "pro"
	if buildRegister(conf).plan != common.PlanPro {
		t.Fatalf("configured plan should be pro")
	}
	conf.Register.Plan = "enterprise"
	if buildRegister(conf).plan != common.PlanFree {
		t.Fatalf("unknown plan should fall back to free")
	}
}

func TestPlanOrDefault(t *testing.T) {
	if got := planOrDefault("plus"); got != common.PlanPlus {
		t.Fatalf("got %v", got)
	}
	if got := planOrDefault("unknown"); got != common.PlanFree {
		t.Fatalf("got %v", got)
	}
}
