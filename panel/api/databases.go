package api

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"log"
	"math/big"
	"net/http"
	"strconv"
	"strings"

	"github.com/arman-arif/control-portal/panel/store"
	"github.com/arman-arif/control-portal/protocol"
)

const passwordChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// newDBPassword returns 24 random letters and digits (about 143 bits).
func newDBPassword() string {
	b := make([]byte, 24)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(passwordChars))))
		if err != nil {
			panic(err)
		}
		b[i] = passwordChars[n.Int64()]
	}
	return string(b)
}

// checkDatabaseServer refuses database work on a server that can't take it
// as far as the panel knows; the agent checks again.
func (a *API) checkDatabaseServer(w http.ResponseWriter, sv store.Server) bool {
	if !sv.Linked {
		writeError(w, http.StatusConflict, "this server is not linked yet")
		return false
	}
	if inv, ok := a.hub.Inventory(sv.ID); ok && inv.MySQL == "" && inv.MariaDB == "" {
		writeError(w, http.StatusConflict, "neither MySQL nor MariaDB is installed on this server; install one from the Stack tab")
		return false
	}
	return true
}

func (a *API) handleListDatabases(w http.ResponseWriter, r *http.Request) {
	sv, ok := a.serverFromPath(w, r)
	if !ok {
		return
	}
	dbs, err := a.store.ListDatabases(r.Context(), userFrom(r).ID, sv.ID)
	if err != nil {
		internalError(w, err)
		return
	}
	users, err := a.store.ListDBUsers(r.Context(), userFrom(r).ID, sv.ID)
	if err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"databases": dbs, "users": users})
}

// handleCreateDatabase creates a database and, with create_user, a user of
// the same name that may use it.
func (a *API) handleCreateDatabase(w http.ResponseWriter, r *http.Request) {
	sv, ok := a.serverFromPath(w, r)
	if !ok {
		return
	}
	var in struct {
		Name       string `json:"name"`
		CreateUser bool   `json:"create_user"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	name := strings.TrimSpace(in.Name)
	if err := protocol.ValidateDBName(name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !a.checkDatabaseServer(w, sv) {
		return
	}
	userID := userFrom(r).ID
	db, err := a.store.CreateDatabase(r.Context(), userID, sv.ID, name)
	if errors.Is(err, store.ErrNameTaken) {
		writeError(w, http.StatusConflict, "this server already has a database named "+name)
		return
	} else if err != nil {
		internalError(w, err)
		return
	}
	if _, err := a.jobs.Enqueue(r.Context(), userID, sv.ID, store.DatabaseRef(db.ID), protocol.ActionDBCreate, protocol.DBParams{Name: name}); err != nil {
		internalError(w, err)
		return
	}
	out := map[string]any{"database": db}
	if in.CreateUser {
		user, password, status, msg := a.createDBUser(r, sv, name, []int64{db.ID})
		if status != 0 {
			out["user_error"] = msg
		} else {
			out["user"], out["password"] = user, password
		}
	}
	writeJSON(w, http.StatusAccepted, out)
}

func (a *API) databaseFromPath(w http.ResponseWriter, r *http.Request) (store.Database, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusNotFound, "database not found")
		return store.Database{}, false
	}
	db, err := a.store.GetDatabase(r.Context(), userFrom(r).ID, id)
	if isNotFound(err) {
		writeError(w, http.StatusNotFound, "database not found")
		return db, false
	} else if err != nil {
		internalError(w, err)
		return db, false
	}
	return db, true
}

func (a *API) handleDeleteDatabase(w http.ResponseWriter, r *http.Request) {
	db, ok := a.databaseFromPath(w, r)
	if !ok {
		return
	}
	userID := userFrom(r).ID
	if err := a.store.SetDatabaseStatus(r.Context(), userID, db.ID, store.DBDeleting); err != nil {
		internalError(w, err)
		return
	}
	job, err := a.jobs.Enqueue(r.Context(), userID, db.ServerID, store.DatabaseRef(db.ID), protocol.ActionDBDrop, protocol.DBParams{Name: db.Name})
	if err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"job": job})
}

// createDBUser stores a user with a new password and queues its creation. A
// non-zero status means it failed, with msg to show.
func (a *API) createDBUser(r *http.Request, sv store.Server, name string, databaseIDs []int64) (store.DBUser, string, int, string) {
	userID := userFrom(r).ID
	ids, names, err := a.store.ServerDatabaseIDs(r.Context(), userID, sv.ID, databaseIDs)
	if err != nil {
		log.Printf("internal error: %v", err)
		return store.DBUser{}, "", http.StatusInternalServerError, "internal error"
	}
	if len(ids) != len(databaseIDs) {
		return store.DBUser{}, "", http.StatusBadRequest, "some of the databases are not on this server"
	}
	password := newDBPassword()
	user, err := a.store.CreateDBUser(r.Context(), userID, sv.ID, name, a.cfg.Secrets.Seal([]byte(password)), ids)
	if errors.Is(err, store.ErrNameTaken) {
		return store.DBUser{}, "", http.StatusConflict, "this server already has a database user named " + name
	} else if err != nil {
		log.Printf("internal error: %v", err)
		return store.DBUser{}, "", http.StatusInternalServerError, "internal error"
	}
	// The password is added when the job is sent; see prepareJob.
	params := protocol.DBUserParams{Name: name, Databases: names}
	if _, err := a.jobs.Enqueue(r.Context(), userID, sv.ID, store.DBUserRef(user.ID), protocol.ActionDBUserCreate, params); err != nil {
		log.Printf("internal error: %v", err)
		return store.DBUser{}, "", http.StatusInternalServerError, "internal error"
	}
	return user, password, 0, ""
}

func (a *API) handleCreateDBUser(w http.ResponseWriter, r *http.Request) {
	sv, ok := a.serverFromPath(w, r)
	if !ok {
		return
	}
	var in struct {
		Name        string  `json:"name"`
		DatabaseIDs []int64 `json:"database_ids"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	name := strings.TrimSpace(in.Name)
	if err := protocol.ValidateDBName(name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !a.checkDatabaseServer(w, sv) {
		return
	}
	user, password, status, msg := a.createDBUser(r, sv, name, in.DatabaseIDs)
	if status != 0 {
		writeError(w, status, msg)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"user": user, "password": password})
}

func (a *API) dbUserFromPath(w http.ResponseWriter, r *http.Request) (store.DBUser, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusNotFound, "database user not found")
		return store.DBUser{}, false
	}
	u, err := a.store.GetDBUser(r.Context(), userFrom(r).ID, id)
	if isNotFound(err) {
		writeError(w, http.StatusNotFound, "database user not found")
		return u, false
	} else if err != nil {
		internalError(w, err)
		return u, false
	}
	return u, true
}

// handleSetDBUserDatabases replaces the databases a user may use.
func (a *API) handleSetDBUserDatabases(w http.ResponseWriter, r *http.Request) {
	u, ok := a.dbUserFromPath(w, r)
	if !ok {
		return
	}
	if u.Status == store.DBDeleting {
		writeError(w, http.StatusConflict, "this user is being deleted")
		return
	}
	var in struct {
		DatabaseIDs []int64 `json:"database_ids"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	userID := userFrom(r).ID
	ids, names, err := a.store.ServerDatabaseIDs(r.Context(), userID, u.ServerID, in.DatabaseIDs)
	if err != nil {
		internalError(w, err)
		return
	}
	if len(ids) != len(in.DatabaseIDs) {
		writeError(w, http.StatusBadRequest, "some of the databases are not on this server")
		return
	}
	u, err = a.store.SetDBUserGrants(r.Context(), userID, u.ID, ids)
	if err != nil {
		internalError(w, err)
		return
	}
	params := protocol.DBUserParams{Name: u.Name, Databases: names}
	if _, err := a.jobs.Enqueue(r.Context(), userID, u.ServerID, store.DBUserRef(u.ID), protocol.ActionDBUserGrants, params); err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"user": u})
}

// handleRevealDBPassword returns a user's password, which the panel keeps
// encrypted.
func (a *API) handleRevealDBPassword(w http.ResponseWriter, r *http.Request) {
	u, ok := a.dbUserFromPath(w, r)
	if !ok {
		return
	}
	password, err := a.dbPassword(r.Context(), u.ID)
	if err != nil {
		internalError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"password": password})
}

func (a *API) dbPassword(ctx context.Context, dbUserID int64) (string, error) {
	sealed, err := a.store.DBUserPassword(ctx, dbUserID)
	if err != nil {
		return "", err
	}
	pw, err := a.cfg.Secrets.Open(sealed)
	if err != nil {
		return "", err
	}
	return string(pw), nil
}

// handleResetDBPassword gives a user a new password on the server.
func (a *API) handleResetDBPassword(w http.ResponseWriter, r *http.Request) {
	u, ok := a.dbUserFromPath(w, r)
	if !ok {
		return
	}
	if u.Status == store.DBDeleting {
		writeError(w, http.StatusConflict, "this user is being deleted")
		return
	}
	userID := userFrom(r).ID
	password := newDBPassword()
	if err := a.store.SetDBUserPassword(r.Context(), userID, u.ID, a.cfg.Secrets.Seal([]byte(password))); err != nil {
		internalError(w, err)
		return
	}
	params := protocol.DBUserParams{Name: u.Name, Databases: u.Databases}
	if _, err := a.jobs.Enqueue(r.Context(), userID, u.ServerID, store.DBUserRef(u.ID), protocol.ActionDBUserCreate, params); err != nil {
		internalError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusAccepted, map[string]string{"password": password})
}

func (a *API) handleDeleteDBUser(w http.ResponseWriter, r *http.Request) {
	u, ok := a.dbUserFromPath(w, r)
	if !ok {
		return
	}
	userID := userFrom(r).ID
	if err := a.store.SetDBUserStatus(r.Context(), userID, u.ID, store.DBDeleting); err != nil {
		internalError(w, err)
		return
	}
	job, err := a.jobs.Enqueue(r.Context(), userID, u.ServerID, store.DBUserRef(u.ID), protocol.ActionDBUserDrop, protocol.DBUserParams{Name: u.Name, Databases: []string{}})
	if err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"job": job})
}

// prepareJob adds what a job needs but its stored params must not hold: the
// password of a database user, read when the job is sent.
func (a *API) prepareJob(ctx context.Context, job store.Job) (json.RawMessage, error) {
	if job.Action != protocol.ActionDBUserCreate || job.DBUserID == nil {
		return job.Params, nil
	}
	var p protocol.DBUserParams
	if err := json.Unmarshal(job.Params, &p); err != nil {
		return nil, err
	}
	pw, err := a.dbPassword(ctx, *job.DBUserID)
	if err != nil {
		return nil, err
	}
	p.Password = pw
	return json.Marshal(p)
}

// databaseJobFinished keeps databases and their users in step with their jobs.
func (a *API) databaseJobFinished(ctx context.Context, job store.Job) {
	ok := job.Status == store.JobSucceeded
	var err error
	switch {
	case job.DatabaseID != nil && job.Action == protocol.ActionDBDrop && ok:
		err = a.store.DeleteDatabaseRow(ctx, *job.DatabaseID)
	case job.DatabaseID != nil && ok:
		err = a.store.DatabaseJobFinished(ctx, *job.DatabaseID, store.DBActive, "")
	case job.DatabaseID != nil:
		err = a.store.DatabaseJobFinished(ctx, *job.DatabaseID, store.DBFailed, job.Error)
	case job.DBUserID != nil && job.Action == protocol.ActionDBUserDrop && ok:
		err = a.store.DeleteDBUserRow(ctx, *job.DBUserID)
	case job.DBUserID != nil && ok:
		err = a.store.DBUserJobFinished(ctx, *job.DBUserID, store.DBActive, "")
	case job.DBUserID != nil:
		err = a.store.DBUserJobFinished(ctx, *job.DBUserID, store.DBFailed, job.Error)
	}
	if err != nil {
		log.Printf("updating after job %d: %v", job.ID, err)
	}
}
