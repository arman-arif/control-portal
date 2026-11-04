package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Statuses of databases and database users, which follow their jobs like
// site statuses do.
const (
	DBCreating = "creating"
	DBUpdating = "updating" // users only
	DBDeleting = "deleting"
	DBActive   = "active"
	DBFailed   = "failed"
)

var ErrNameTaken = errors.New("name already used on this server")

type Database struct {
	ID        int64     `json:"id"`
	ServerID  int64     `json:"server_id"`
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	Error     string    `json:"error"`
	CreatedAt time.Time `json:"created_at"`
	Users     []string  `json:"users"` // names of the users with access
}

type DBUser struct {
	ID          int64     `json:"id"`
	ServerID    int64     `json:"server_id"`
	Name        string    `json:"name"`
	Status      string    `json:"status"`
	Error       string    `json:"error"`
	CreatedAt   time.Time `json:"created_at"`
	DatabaseIDs []int64   `json:"database_ids"`
	Databases   []string  `json:"databases"` // names, in the same order
}

const databaseCols = `d.id, d.server_id, d.name, d.status, d.error, d.created_at,
	COALESCE((SELECT array_agg(u.name ORDER BY u.name) FROM database_grants g
		JOIN database_users u ON u.id = g.database_user_id WHERE g.database_id = d.id), '{}')`

func scanDatabase(row pgx.Row) (Database, error) {
	var d Database
	err := row.Scan(&d.ID, &d.ServerID, &d.Name, &d.Status, &d.Error, &d.CreatedAt, &d.Users)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, ErrNotFound
	}
	return d, err
}

const dbUserCols = `u.id, u.server_id, u.name, u.status, u.error, u.created_at,
	COALESCE((SELECT array_agg(d.id ORDER BY d.name) FROM database_grants g
		JOIN databases d ON d.id = g.database_id WHERE g.database_user_id = u.id), '{}'),
	COALESCE((SELECT array_agg(d.name ORDER BY d.name) FROM database_grants g
		JOIN databases d ON d.id = g.database_id WHERE g.database_user_id = u.id), '{}')`

func scanDBUser(row pgx.Row) (DBUser, error) {
	var u DBUser
	err := row.Scan(&u.ID, &u.ServerID, &u.Name, &u.Status, &u.Error, &u.CreatedAt, &u.DatabaseIDs, &u.Databases)
	if errors.Is(err, pgx.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}

func (s *Store) CreateDatabase(ctx context.Context, userID, serverID int64, name string) (Database, error) {
	var id int64
	err := s.db.QueryRow(ctx, `INSERT INTO databases(server_id, user_id, name) VALUES ($1, $2, $3) RETURNING id`,
		serverID, userID, name).Scan(&id)
	if uniqueViolation(err) != "" {
		return Database{}, ErrNameTaken
	} else if err != nil {
		return Database{}, err
	}
	return s.GetDatabase(ctx, userID, id)
}

func (s *Store) GetDatabase(ctx context.Context, userID, id int64) (Database, error) {
	return scanDatabase(s.db.QueryRow(ctx, `SELECT `+databaseCols+` FROM databases d WHERE d.id=$1 AND d.user_id=$2`, id, userID))
}

func (s *Store) ListDatabases(ctx context.Context, userID, serverID int64) ([]Database, error) {
	rows, err := s.db.Query(ctx, `SELECT `+databaseCols+` FROM databases d WHERE d.user_id=$1 AND d.server_id=$2 ORDER BY d.name`, userID, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Database{}
	for rows.Next() {
		d, err := scanDatabase(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) SetDatabaseStatus(ctx context.Context, userID, id int64, status string) error {
	tag, err := s.db.Exec(ctx, `UPDATE databases SET status=$3, error='' WHERE id=$1 AND user_id=$2`, id, userID, status)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// DatabaseJobFinished records the outcome of a database's job unless more of
// its jobs are waiting.
func (s *Store) DatabaseJobFinished(ctx context.Context, id int64, status, errMsg string) error {
	_, err := s.db.Exec(ctx, `UPDATE databases SET status=$2, error=$3 WHERE id=$1 AND NOT EXISTS
		(SELECT 1 FROM jobs WHERE database_id=$1 AND status IN ('queued', 'running'))`, id, status, errMsg)
	return err
}

// DeleteDatabaseRow removes a database once the server has dropped it.
func (s *Store) DeleteDatabaseRow(ctx context.Context, id int64) error {
	_, err := s.db.Exec(ctx, `DELETE FROM databases WHERE id=$1 AND NOT EXISTS
		(SELECT 1 FROM jobs WHERE database_id=$1 AND status IN ('queued', 'running'))`, id)
	return err
}

// ServerDatabaseIDs keeps the ids that are databases of the user on the server
// and returns them with their names.
func (s *Store) ServerDatabaseIDs(ctx context.Context, userID, serverID int64, ids []int64) ([]int64, []string, error) {
	rows, err := s.db.Query(ctx, `SELECT id, name FROM databases WHERE user_id=$1 AND server_id=$2 AND id = ANY($3) ORDER BY name`,
		userID, serverID, ids)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	keptIDs, names := []int64{}, []string{}
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, nil, err
		}
		keptIDs, names = append(keptIDs, id), append(names, name)
	}
	return keptIDs, names, rows.Err()
}

func setGrants(ctx context.Context, tx pgx.Tx, dbUserID int64, databaseIDs []int64) error {
	if _, err := tx.Exec(ctx, `DELETE FROM database_grants WHERE database_user_id=$1`, dbUserID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO database_grants(database_id, database_user_id) SELECT unnest($2::bigint[]), $1`, dbUserID, databaseIDs)
	return err
}

// CreateDBUser stores a user with its sealed password and the databases it
// may use, which the caller has checked belong to the server.
func (s *Store) CreateDBUser(ctx context.Context, userID, serverID int64, name string, sealedPassword []byte, databaseIDs []int64) (DBUser, error) {
	var id int64
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `INSERT INTO database_users(server_id, user_id, name, password) VALUES ($1, $2, $3, $4) RETURNING id`,
			serverID, userID, name, sealedPassword).Scan(&id)
		if err != nil {
			return err
		}
		return setGrants(ctx, tx, id, databaseIDs)
	})
	if uniqueViolation(err) != "" {
		return DBUser{}, ErrNameTaken
	} else if err != nil {
		return DBUser{}, err
	}
	return s.GetDBUser(ctx, userID, id)
}

func (s *Store) GetDBUser(ctx context.Context, userID, id int64) (DBUser, error) {
	return scanDBUser(s.db.QueryRow(ctx, `SELECT `+dbUserCols+` FROM database_users u WHERE u.id=$1 AND u.user_id=$2`, id, userID))
}

func (s *Store) ListDBUsers(ctx context.Context, userID, serverID int64) ([]DBUser, error) {
	rows, err := s.db.Query(ctx, `SELECT `+dbUserCols+` FROM database_users u WHERE u.user_id=$1 AND u.server_id=$2 ORDER BY u.name`, userID, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DBUser{}
	for rows.Next() {
		u, err := scanDBUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// DBUserPassword returns a user's sealed password. It isn't scoped to an
// account: the job runner calls it for jobs it has already accepted.
func (s *Store) DBUserPassword(ctx context.Context, id int64) ([]byte, error) {
	var sealed []byte
	err := s.db.QueryRow(ctx, `SELECT password FROM database_users WHERE id=$1`, id).Scan(&sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return sealed, err
}

// SetDBUserGrants replaces the databases a user may use and marks it as
// updating.
func (s *Store) SetDBUserGrants(ctx context.Context, userID, id int64, databaseIDs []int64) (DBUser, error) {
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE database_users SET status='updating', error='' WHERE id=$1 AND user_id=$2`, id, userID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return setGrants(ctx, tx, id, databaseIDs)
	})
	if err != nil {
		return DBUser{}, err
	}
	return s.GetDBUser(ctx, userID, id)
}

func (s *Store) SetDBUserPassword(ctx context.Context, userID, id int64, sealedPassword []byte) error {
	tag, err := s.db.Exec(ctx, `UPDATE database_users SET password=$3, status='updating', error='' WHERE id=$1 AND user_id=$2`,
		id, userID, sealedPassword)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (s *Store) SetDBUserStatus(ctx context.Context, userID, id int64, status string) error {
	tag, err := s.db.Exec(ctx, `UPDATE database_users SET status=$3, error='' WHERE id=$1 AND user_id=$2`, id, userID, status)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (s *Store) DBUserJobFinished(ctx context.Context, id int64, status, errMsg string) error {
	_, err := s.db.Exec(ctx, `UPDATE database_users SET status=$2, error=$3 WHERE id=$1 AND NOT EXISTS
		(SELECT 1 FROM jobs WHERE database_user_id=$1 AND status IN ('queued', 'running'))`, id, status, errMsg)
	return err
}

func (s *Store) DeleteDBUserRow(ctx context.Context, id int64) error {
	_, err := s.db.Exec(ctx, `DELETE FROM database_users WHERE id=$1 AND NOT EXISTS
		(SELECT 1 FROM jobs WHERE database_user_id=$1 AND status IN ('queued', 'running'))`, id)
	return err
}
