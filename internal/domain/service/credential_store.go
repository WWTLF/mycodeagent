package service

// CredentialStore is the domain abstraction over the persisted credential
// blob (vast.ai API key + HuggingFace token). It exists so the App layer can
// orchestrate the login flow without directly importing infrastructure/config.
//
// Implementations live under internal/infrastructure/config/.
type CredentialStore interface {
	// SaveCredentials persists the supplied credentials. Empty strings are
	// treated as "leave the existing value alone" so the login command can
	// update one credential without touching the other.
	SaveCredentials(apiKey, hfToken string) error
}
