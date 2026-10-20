package user

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"edu.agent.code/adaptor/repo/model"
	"edu.agent.code/service/do"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "user.db")
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}
	return db
}

func TestCreateAndGetByEmailNormalizesCase(t *testing.T) {
	repo := NewUserWithDB(newTestDB(t))
	ctx := context.Background()

	record := &do.User{
		Email:        "  Student@Example.COM ",
		PasswordHash: "hash",
		Plan:         "free",
	}
	if err := repo.Create(ctx, record); err != nil {
		t.Fatalf("create: %v", err)
	}
	if record.ID == 0 {
		t.Fatalf("expected auto increment id to be filled back")
	}
	if record.Email != "student@example.com" {
		t.Fatalf("email not normalized: %q", record.Email)
	}

	got, err := repo.GetByEmail(ctx, "STUDENT@example.com ")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("expected user, got nil")
	}
	if got.Email != "student@example.com" || got.PasswordHash != "hash" || got.Plan != "free" {
		t.Fatalf("unexpected user: %+v", got)
	}
	if !got.CreatedAt.After(time.Time{}) {
		t.Fatalf("expected created_at to be set")
	}
}

func TestGetByEmailMissingReturnsNil(t *testing.T) {
	repo := NewUserWithDB(newTestDB(t))
	got, err := repo.GetByEmail(context.Background(), "nobody@example.com")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
}

func TestDuplicateEmailIsUniqueViolation(t *testing.T) {
	repo := NewUserWithDB(newTestDB(t))
	ctx := context.Background()
	first := &do.User{Email: "dup@example.com", PasswordHash: "h1", Plan: "free"}
	if err := repo.Create(ctx, first); err != nil {
		t.Fatalf("create first: %v", err)
	}
	second := &do.User{Email: "DUP@example.com", PasswordHash: "h2", Plan: "plus"}
	err := repo.Create(ctx, second)
	if err == nil {
		t.Fatal("expected duplicate error")
	}
	if !IsUniqueViolation(err) {
		t.Fatalf("expected unique violation, got %v", err)
	}
}

func TestUpdatePasswordNormalizesEmail(t *testing.T) {
	repo := NewUserWithDB(newTestDB(t))
	ctx := context.Background()
	record := &do.User{Email: "reset@example.com", PasswordHash: "old", Plan: "free"}
	if err := repo.Create(ctx, record); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := repo.UpdatePassword(ctx, "RESET@example.com ", "new-hash"); err != nil {
		t.Fatalf("update password: %v", err)
	}
	got, err := repo.GetByEmail(ctx, "reset@example.com")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.PasswordHash != "new-hash" {
		t.Fatalf("password hash = %q, want new-hash", got.PasswordHash)
	}
}

func TestUpdateLastLogin(t *testing.T) {
	repo := NewUserWithDB(newTestDB(t))
	ctx := context.Background()
	record := &do.User{Email: "login@example.com", PasswordHash: "h", Plan: "free"}
	if err := repo.Create(ctx, record); err != nil {
		t.Fatalf("create: %v", err)
	}
	at := time.Now().Round(time.Second)
	if err := repo.UpdateLastLogin(ctx, "LOGIN@example.com", at); err != nil {
		t.Fatalf("update last login: %v", err)
	}
	got, err := repo.GetByEmail(ctx, "login@example.com")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.LastLoginAt == nil || !got.LastLoginAt.Equal(at) {
		t.Fatalf("last_login_at = %v, want %v", got.LastLoginAt, at)
	}
}
