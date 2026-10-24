// Package policy defines the action intent shape evaluated at the policy boundary.
package policy

// ActionIntent describes one autonomous or human initiated change request.
type ActionIntent struct {
	Actor         Actor    `json:"actor"`
	Target        Target   `json:"target"`
	ModifiedFiles []string `json:"modified_files"`
	Diff          Diff     `json:"diff"`
	Approved      bool     `json:"approved,omitempty"`
}

type Actor struct {
	Type string `json:"type"`
	ID   string `json:"id,omitempty"`
}
type Target struct {
	Env string `json:"env"`
}
type Diff struct {
	ModifiesTests string `json:"modifies_tests"`
}

// Input returns a normalized JSON-compatible policy input with explicit defaults.
func (i ActionIntent) Input() map[string]any {
	modified := i.ModifiedFiles
	if modified == nil {
		modified = []string{}
	}
	return map[string]any{
		"actor":          map[string]any{"type": i.Actor.Type, "id": i.Actor.ID},
		"target":         map[string]any{"env": i.Target.Env},
		"modified_files": modified,
		"diff":           map[string]any{"modifies_tests": i.Diff.ModifiesTests},
	}
}
