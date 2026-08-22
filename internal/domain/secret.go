package domain

import "time"

// SecretMetadata is the only secret representation exposed through management
// surfaces. Plaintext and ciphertext never appear in API responses.
type SecretMetadata struct {
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Version     int64     `json:"version"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type SecretReference struct {
	Name    string
	Purpose string
}

// SecretReferences returns every named secret required by a revision. The
// purpose is persisted for operator diagnostics but is never used as authority.
func (m Manifest) SecretReferences() []SecretReference {
	references := make([]SecretReference, 0, len(m.Runtime.Secrets)+len(m.Triggers))
	for target, name := range m.Runtime.Secrets {
		references = append(references, SecretReference{Name: name, Purpose: "runtime:" + target})
	}
	for _, trigger := range m.Triggers {
		var name string
		switch trigger.Type {
		case "webhook":
			name, _ = trigger.Config["secret"].(string)
		case "email", "ntfy":
			name, _ = trigger.Config["tokenSecret"].(string)
		}
		if name != "" {
			references = append(references, SecretReference{Name: name, Purpose: "trigger:" + trigger.ID})
		}
	}
	return references
}
