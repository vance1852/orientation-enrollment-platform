package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/vance1852/orientation-enrollment-platform/internal/domain"
)

const userColumns = `id, email, display_name, role, password_hash, disabled, created_at, updated_at`

// CreateUser inserts a principal. A duplicate email is reported as a business
// conflict rather than a driver specific constraint error.
func (d *dataset) CreateUser(ctx context.Context, user domain.User) (domain.User, error) {
	email, err := domain.NormalizeEmail(user.Email)
	if err != nil {
		return domain.User{}, err
	}
	if !user.Role.Valid() {
		return domain.User{}, domain.NewFieldError("role", "must be student or registrar")
	}
	if user.PasswordHash == "" {
		return domain.User{}, domain.NewFieldError("password", "must not be empty")
	}
	res, err := d.q.ExecContext(ctx, `
        INSERT INTO users (email, display_name, role, password_hash, disabled, created_at, updated_at)
        VALUES (?, ?, ?, ?, ?, ?, ?)`,
		email, user.DisplayName, string(user.Role), user.PasswordHash,
		boolToInt(user.Disabled), formatTime(user.CreatedAt), formatTime(user.UpdatedAt))
	if err != nil {
		if isUniqueViolation(err) {
			return domain.User{}, fmt.Errorf("user %s already exists: %w", email, domain.ErrConflict)
		}
		return domain.User{}, fmt.Errorf("insert user: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return domain.User{}, fmt.Errorf("read inserted user id: %w", err)
	}
	user.ID = id
	user.Email = email
	return user, nil
}

// FindUserByEmail loads a principal by normalised email address.
func (d *dataset) FindUserByEmail(ctx context.Context, email string) (domain.User, error) {
	normalized, err := domain.NormalizeEmail(email)
	if err != nil {
		return domain.User{}, err
	}
	row := d.q.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE email = ?`, normalized)
	user, err := scanUser(row)
	if err != nil {
		return domain.User{}, notFound("user "+normalized, err)
	}
	return user, nil
}

// FindUserByID loads a principal by identifier.
func (d *dataset) FindUserByID(ctx context.Context, id int64) (domain.User, error) {
	row := d.q.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = ?`, id)
	user, err := scanUser(row)
	if err != nil {
		return domain.User{}, notFound(fmt.Sprintf("user %d", id), err)
	}
	return user, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanUser(row rowScanner) (domain.User, error) {
	var (
		user      domain.User
		role      string
		disabled  int
		createdAt string
		updatedAt string
	)
	if err := row.Scan(&user.ID, &user.Email, &user.DisplayName, &role, &user.PasswordHash,
		&disabled, &createdAt, &updatedAt); err != nil {
		return domain.User{}, err
	}
	user.Role = domain.Role(role)
	user.Disabled = disabled != 0

	var err error
	if user.CreatedAt, err = parseTime(createdAt); err != nil {
		return domain.User{}, err
	}
	if user.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return domain.User{}, err
	}
	return user, nil
}

// countRows is a small helper shared by the paginated list queries.
func countRows(ctx context.Context, q querier, query string, args ...any) (int, error) {
	var total int
	if err := q.QueryRowContext(ctx, query, args...).Scan(&total); err != nil {
		if err == sql.ErrNoRows {
			return 0, nil
		}
		return 0, fmt.Errorf("count rows: %w", err)
	}
	return total, nil
}
