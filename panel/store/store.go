// Package store is the panel's PostgreSQL data layer.
package store

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

var (
	ErrNotFound   = errors.New("not found")
	ErrEmailTaken = errors.New("email already registered")
)

type Store struct {
	db *pgxpool.Pool
}

func Open(ctx context.Context, url string) (*Store, error) {
	db, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("connecting to database: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() { s.db.Close() }

// Migrate applies any migrations in migrations/ that have not run yet, in
// filename order, each in its own transaction.
func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.db.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		name TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	names, err := fs.Glob(migrationFS, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	for _, path := range names {
		name := strings.TrimPrefix(path, "migrations/")
		var exists bool
		if err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE name=$1)`, name).Scan(&exists); err != nil {
			return err
		}
		if exists {
			continue
		}
		sql, err := migrationFS.ReadFile(path)
		if err != nil {
			return err
		}
		err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, string(sql)); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `INSERT INTO schema_migrations(name) VALUES ($1)`, name)
			return err
		})
		if err != nil {
			return fmt.Errorf("migration %s: %w", name, err)
		}
	}
	return nil
}

// ---- users ----

type User struct {
	ID        int64     `json:"id"`
	Email     string    `json:"email"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n)
	return n, err
}

func (s *Store) CreateUser(ctx context.Context, email, passwordHash string) (User, error) {
	u := User{Email: email}
	err := s.db.QueryRow(ctx,
		`INSERT INTO users(email, password_hash) VALUES ($1, $2) RETURNING id, created_at`,
		email, passwordHash).Scan(&u.ID, &u.CreatedAt)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return u, ErrEmailTaken
	}
	return u, err
}

// UserByEmail returns the user and their password hash.
func (s *Store) UserByEmail(ctx context.Context, email string) (User, string, error) {
	var u User
	var hash string
	err := s.db.QueryRow(ctx,
		`SELECT id, email, created_at, password_hash FROM users WHERE email=$1`, email).
		Scan(&u.ID, &u.Email, &u.CreatedAt, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return u, "", ErrNotFound
	}
	return u, hash, err
}

// ---- sessions ----

func (s *Store) CreateSession(ctx context.Context, userID int64, tokenHash []byte, expires time.Time) error {
	_, err := s.db.Exec(ctx,
		`INSERT INTO sessions(token_hash, user_id, expires_at) VALUES ($1, $2, $3)`,
		tokenHash, userID, expires)
	return err
}

func (s *Store) SessionUser(ctx context.Context, tokenHash []byte) (User, error) {
	var u User
	err := s.db.QueryRow(ctx, `SELECT u.id, u.email, u.created_at FROM sessions s
		JOIN users u ON u.id = s.user_id
		WHERE s.token_hash=$1 AND s.expires_at > now()`, tokenHash).
		Scan(&u.ID, &u.Email, &u.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}

func (s *Store) DeleteSession(ctx context.Context, tokenHash []byte) error {
	_, err := s.db.Exec(ctx, `DELETE FROM sessions WHERE token_hash=$1`, tokenHash)
	return err
}

func (s *Store) DeleteExpiredSessions(ctx context.Context) error {
	_, err := s.db.Exec(ctx, `DELETE FROM sessions WHERE expires_at <= now()`)
	return err
}

// ---- servers ----

type Server struct {
	ID           int64      `json:"id"`
	Name         string     `json:"name"`
	Hostname     string     `json:"hostname"`
	OS           string     `json:"os"`
	Kernel       string     `json:"kernel"`
	Arch         string     `json:"arch"`
	CPUs         int        `json:"cpus"`
	AgentVersion string     `json:"agent_version"`
	IP           string     `json:"ip"`
	Linked       bool       `json:"linked"`
	LinkedAt     *time.Time `json:"linked_at"`
	LastSeenAt   *time.Time `json:"last_seen_at"`
	CreatedAt    time.Time  `json:"created_at"`
}

const serverCols = `id, name, hostname, os, kernel, arch, cpus, agent_version, ip,
	agent_secret_hash IS NOT NULL, linked_at, last_seen_at, created_at`

func scanServer(row pgx.Row) (Server, error) {
	var sv Server
	err := row.Scan(&sv.ID, &sv.Name, &sv.Hostname, &sv.OS, &sv.Kernel, &sv.Arch, &sv.CPUs,
		&sv.AgentVersion, &sv.IP, &sv.Linked, &sv.LinkedAt, &sv.LastSeenAt, &sv.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return sv, ErrNotFound
	}
	return sv, err
}

func (s *Store) CreateServer(ctx context.Context, userID int64, name string, tokenHash []byte, tokenExpires time.Time) (Server, error) {
	return scanServer(s.db.QueryRow(ctx, `INSERT INTO servers(user_id, name, install_token_hash, install_token_expires_at)
		VALUES ($1, $2, $3, $4) RETURNING `+serverCols, userID, name, tokenHash, tokenExpires))
}

func (s *Store) ListServers(ctx context.Context, userID int64) ([]Server, error) {
	rows, err := s.db.Query(ctx, `SELECT `+serverCols+` FROM servers WHERE user_id=$1 ORDER BY name, id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	servers := []Server{}
	for rows.Next() {
		sv, err := scanServer(rows)
		if err != nil {
			return nil, err
		}
		servers = append(servers, sv)
	}
	return servers, rows.Err()
}

func (s *Store) GetServer(ctx context.Context, userID, id int64) (Server, error) {
	return scanServer(s.db.QueryRow(ctx, `SELECT `+serverCols+` FROM servers WHERE id=$1 AND user_id=$2`, id, userID))
}

func (s *Store) DeleteServer(ctx context.Context, userID, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM servers WHERE id=$1 AND user_id=$2`, id, userID)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// ResetInstallToken replaces the install token of a server that has not been
// linked yet.
func (s *Store) ResetInstallToken(ctx context.Context, userID, id int64, tokenHash []byte, expires time.Time) error {
	tag, err := s.db.Exec(ctx, `UPDATE servers SET install_token_hash=$3, install_token_expires_at=$4
		WHERE id=$1 AND user_id=$2 AND agent_secret_hash IS NULL`, id, userID, tokenHash, expires)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// LinkAgent consumes a valid install token and stores the agent's secret.
// The token can only be used once.
func (s *Store) LinkAgent(ctx context.Context, tokenHash, secretHash []byte, hostname, ip string) (int64, error) {
	var id int64
	err := s.db.QueryRow(ctx, `UPDATE servers SET agent_secret_hash=$2, install_token_hash=NULL,
			install_token_expires_at=NULL, hostname=$3, ip=$4, linked_at=now()
		WHERE install_token_hash=$1 AND install_token_expires_at > now() AND agent_secret_hash IS NULL
		RETURNING id`, tokenHash, secretHash, hostname, ip).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	return id, err
}

func (s *Store) ServerIDByAgentSecret(ctx context.Context, secretHash []byte) (int64, error) {
	var id int64
	err := s.db.QueryRow(ctx, `SELECT id FROM servers WHERE agent_secret_hash=$1`, secretHash).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	return id, err
}

type AgentInfo struct {
	Hostname, OS, Kernel, Arch, AgentVersion, IP string
	CPUs                                         int
}

func (s *Store) UpdateAgentInfo(ctx context.Context, id int64, in AgentInfo) error {
	_, err := s.db.Exec(ctx, `UPDATE servers SET hostname=$2, os=$3, kernel=$4, arch=$5, cpus=$6,
		agent_version=$7, ip=$8, last_seen_at=now() WHERE id=$1`,
		id, in.Hostname, in.OS, in.Kernel, in.Arch, in.CPUs, in.AgentVersion, in.IP)
	return err
}

func (s *Store) TouchServer(ctx context.Context, id int64) error {
	_, err := s.db.Exec(ctx, `UPDATE servers SET last_seen_at=now() WHERE id=$1`, id)
	return err
}
