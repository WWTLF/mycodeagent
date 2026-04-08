package config

import "github.com/WWTLF/mycodeagent/internal/domain/service"

// Store implements service.CredentialStore by reading and writing the YAML
// config file under ~/.mycodeagent/config.yaml. It is the only place where
// credential persistence happens after the refactor — App.Login orchestrates
// the verify+upload+save flow but never touches Save() directly.
type Store struct{}

var _ service.CredentialStore = (*Store)(nil)

func NewStore() *Store {
	return &Store{}
}

// SaveCredentials loads the on-disk config (or starts fresh if none exists),
// updates only the non-empty fields, and writes back. Empty strings are
// treated as "leave existing value alone" so the login command can update one
// credential without disturbing the other.
func (s *Store) SaveCredentials(apiKey, hfToken string) error {
	cfg, err := Load()
	if err != nil {
		// Load returning an error here is rare (it tolerates missing files).
		// Fall back to a fresh config so first-time setup still works.
		cfg = &Config{BasePort: 8000}
	}
	if apiKey != "" {
		cfg.VastaiAPIKey = apiKey
	}
	if hfToken != "" {
		cfg.HFToken = hfToken
	}
	return Save(cfg)
}
