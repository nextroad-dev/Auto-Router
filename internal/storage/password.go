package storage

import (
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

var (
	ErrPasswordNotSet     = errors.New("management password has not been set")
	ErrPasswordAlreadySet = errors.New("management password is already set")
	ErrInvalidPassword    = errors.New("password must be between 12 and 1024 bytes")
	ErrPasswordMismatch   = errors.New("current password is incorrect")
)

const (
	passwordHashIterations = 600_000
	passwordSaltBytes      = 16
	passwordKeyBytes       = 32
	minPasswordBytes       = 12
	maxPasswordBytes       = 1024
)

// PasswordSet reports whether the singleton owner account has a password.
func PasswordSet(ctx context.Context, db *sql.DB) (bool, error) {
	if db == nil {
		return false, errors.New("password database is nil")
	}
	var present int
	err := db.QueryRowContext(ctx, `SELECT count(*) FROM admin_account WHERE id=1`).Scan(&present)
	if err != nil {
		return false, fmt.Errorf("read management password state: %w", err)
	}
	return present != 0, nil
}

// SetInitialPassword atomically creates the single owner password. Competing
// first-run requests can have only one winner.
func SetInitialPassword(ctx context.Context, db *sql.DB, password string) error {
	if db == nil {
		return errors.New("password database is nil")
	}
	if err := validatePassword(password); err != nil {
		return err
	}
	hash, err := makePasswordHash(password)
	if err != nil {
		return fmt.Errorf("hash management password: %w", err)
	}
	_, err = db.ExecContext(ctx, `INSERT INTO admin_account(id,password_hash,updated_at) VALUES(1,?,strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, hash)
	if err != nil {
		var exists int
		checkErr := db.QueryRowContext(ctx, `SELECT count(*) FROM admin_account WHERE id=1`).Scan(&exists)
		if checkErr == nil && exists != 0 {
			return ErrPasswordAlreadySet
		}
		return fmt.Errorf("set initial management password: %w", err)
	}
	return nil
}

// VerifyPassword checks the submitted password using a constant-time digest
// comparison. An unset account is an ordinary false result.
func VerifyPassword(ctx context.Context, db *sql.DB, password string) (bool, error) {
	if db == nil {
		return false, errors.New("password database is nil")
	}
	var encoded string
	err := db.QueryRowContext(ctx, `SELECT password_hash FROM admin_account WHERE id=1`).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read management password: %w", err)
	}
	return checkPasswordHash(encoded, password)
}

// ChangePassword verifies the old password and replaces it in one transaction.
func ChangePassword(ctx context.Context, db *sql.DB, currentPassword, newPassword string) error {
	if db == nil {
		return errors.New("password database is nil")
	}
	if err := validatePassword(newPassword); err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin password change: %w", err)
	}
	defer tx.Rollback()
	var existing string
	err = tx.QueryRowContext(ctx, `SELECT password_hash FROM admin_account WHERE id=1`).Scan(&existing)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrPasswordNotSet
	}
	if err != nil {
		return fmt.Errorf("read current management password: %w", err)
	}
	valid, err := checkPasswordHash(existing, currentPassword)
	if err != nil {
		return err
	}
	if !valid {
		return ErrPasswordMismatch
	}
	encoded, err := makePasswordHash(newPassword)
	if err != nil {
		return fmt.Errorf("hash new management password: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE admin_account SET password_hash=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=1`, encoded); err != nil {
		return fmt.Errorf("change management password: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit management password change: %w", err)
	}
	return nil
}

// ResetPassword replaces an existing owner password without requiring the old
// value. It is intended for an offline recovery command after that command has
// independently established that the service is stopped.
func ResetPassword(ctx context.Context, db *sql.DB, newPassword string) error {
	if db == nil {
		return errors.New("password database is nil")
	}
	if err := validatePassword(newPassword); err != nil {
		return err
	}
	encoded, err := makePasswordHash(newPassword)
	if err != nil {
		return fmt.Errorf("hash replacement management password: %w", err)
	}
	result, err := db.ExecContext(ctx, `UPDATE admin_account SET password_hash=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=1`, encoded)
	if err != nil {
		return fmt.Errorf("reset management password: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("count management password reset: %w", err)
	}
	if changed == 0 {
		return ErrPasswordNotSet
	}
	return nil
}

func validatePassword(password string) error {
	if len(password) < minPasswordBytes || len(password) > maxPasswordBytes {
		return ErrInvalidPassword
	}
	return nil
}

func makePasswordHash(password string) (string, error) {
	salt := make([]byte, passwordSaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	derived, err := pbkdf2.Key(sha256.New, password, salt, passwordHashIterations, passwordKeyBytes)
	if err != nil {
		return "", err
	}
	return strings.Join([]string{
		"pbkdf2-sha256",
		strconv.Itoa(passwordHashIterations),
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(derived),
	}, "$"), nil
}

func checkPasswordHash(encoded, password string) (bool, error) {
	if len(password) > maxPasswordBytes {
		return false, nil
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false, errors.New("stored management password hash is corrupt")
	}
	iterations, err := strconv.Atoi(parts[1])
	if err != nil || iterations < 100_000 || iterations > 2_000_000 {
		return false, errors.New("stored management password hash is corrupt")
	}
	salt, saltErr := base64.RawStdEncoding.DecodeString(parts[2])
	want, wantErr := base64.RawStdEncoding.DecodeString(parts[3])
	if saltErr != nil || len(salt) != passwordSaltBytes || wantErr != nil || len(want) != passwordKeyBytes {
		return false, errors.New("stored management password hash is corrupt")
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, iterations, passwordKeyBytes)
	if err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}
