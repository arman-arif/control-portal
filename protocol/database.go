package protocol

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Database actions. Names and passwords are checked by ValidateDBName and
// ValidateDBPassword on both sides, so the agent can put them straight into
// SQL without escaping.
const (
	ActionDBCreate     = "db.create"      // params: DBParams{Name}
	ActionDBDrop       = "db.drop"        // params: DBParams{Name}
	ActionDBUserCreate = "db.user.create" // params: DBUserParams; also sets the password of an existing user
	ActionDBUserGrants = "db.user.grants" // params: DBUserParams without Password
	ActionDBUserDrop   = "db.user.drop"   // params: DBUserParams{Name}
)

type DBParams struct {
	Name string `json:"name"`
}

// DBUserParams describes a database user. Databases is the complete list the
// user may use: the agent revokes everything else.
type DBUserParams struct {
	Name      string   `json:"name"`
	Password  string   `json:"password,omitempty"`
	Databases []string `json:"databases"`
}

var (
	dbNamePattern     = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	dbPasswordPattern = regexp.MustCompile(`^[A-Za-z0-9]{16,64}$`)
	reservedDBNames   = []string{"mysql", "sys", "information_schema", "performance_schema", "root", "admin", "mariadb", "test"}
)

// ValidateDBName accepts names for both databases and users: a lower-case
// letter, then up to 31 lower-case letters, digits and underscores.
func ValidateDBName(name string) error {
	if !dbNamePattern.MatchString(name) {
		return fmt.Errorf("%q must start with a letter and have up to 32 lower-case letters, digits and underscores", name)
	}
	if slices.Contains(reservedDBNames, name) {
		return fmt.Errorf("%q is reserved", name)
	}
	return nil
}

// ValidateDBPassword accepts the passwords the panel generates.
func ValidateDBPassword(pw string) error {
	if !dbPasswordPattern.MatchString(pw) {
		return fmt.Errorf("database passwords must be 16 to 64 letters and digits")
	}
	return nil
}

func (p DBUserParams) Validate(needPassword bool) error {
	if err := ValidateDBName(p.Name); err != nil {
		return err
	}
	if needPassword {
		if err := ValidateDBPassword(p.Password); err != nil {
			return err
		}
	}
	for _, d := range p.Databases {
		if err := ValidateDBName(d); err != nil {
			return err
		}
	}
	return nil
}

// GrantPattern is a database name as it must appear in GRANT and REVOKE, where
// an underscore would otherwise match any character.
func GrantPattern(db string) string {
	return strings.ReplaceAll(db, "_", `\_`)
}
