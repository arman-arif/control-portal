package protocol

import (
	"errors"
	"unicode/utf8"
)

// .env actions. They are answered directly rather than queued, and the panel
// never stores their content: the file lives only on the server.
const (
	ActionSiteEnvRead  = "site.env.read"  // params: EnvParams without Content; result: EnvFile
	ActionSiteEnvWrite = "site.env.write" // params: EnvParams
)

const MaxEnvFile = 256 << 10

type EnvParams struct {
	Site    Site   `json:"site"`
	Content string `json:"content,omitempty"`
}

type EnvFile struct {
	Exists  bool   `json:"exists"`
	Content string `json:"content"`
}

// IsEnvAction reports whether action reads or writes a site's .env file.
func IsEnvAction(action string) bool {
	return action == ActionSiteEnvRead || action == ActionSiteEnvWrite
}

func (p EnvParams) Validate() error {
	if err := p.Site.Validate(); err != nil {
		return err
	}
	if len(p.Content) > MaxEnvFile {
		return errors.New("the .env file is larger than 256 KB")
	}
	if !utf8.ValidString(p.Content) {
		return errors.New("the .env file must be UTF-8 text")
	}
	return nil
}
