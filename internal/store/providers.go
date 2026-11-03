package store

import (
	"database/sql"
	"encoding/json"
	"time"

	"leaflet/internal/doc"
)

// Provider is a model connection. Cloud providers live on the server,
// whose Vault keeps their keys; local ones live on this device only.
type Provider struct {
	ID string
	// Preset is openai | claude | xai | openrouter | deepseek | glm | custom
	// | ollama | lmstudio | local.
	Preset       string
	Name         string
	BaseURL      string
	AuthKind     string // account | api_key | none
	DefaultModel string
	IsLocal      bool
	// SupportsTools is nil until a connection test tells.
	SupportsTools *bool
	Models        []string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

const providerCols = `id, preset, name, base_url, auth_kind, default_model, is_local, supports_tools, models, created_at, updated_at`

func scanProvider(sc scanner) (Provider, error) {
	var p Provider
	var tools sql.NullBool
	var models string
	var c, u int64
	err := sc.Scan(&p.ID, &p.Preset, &p.Name, &p.BaseURL, &p.AuthKind, &p.DefaultModel, &p.IsLocal, &tools, &models, &c, &u)
	if tools.Valid {
		v := tools.Bool
		p.SupportsTools = &v
	}
	_ = json.Unmarshal([]byte(models), &p.Models)
	p.CreatedAt, p.UpdatedAt = time.UnixMilli(c), time.UnixMilli(u)
	return p, err
}

// Providers returns the model connections, oldest first.
func (s *Store) Providers() ([]Provider, error) {
	rows, err := s.db.Query(`SELECT ` + providerCols + ` FROM model_providers ORDER BY created_at`)
	return collect(rows, err, scanProvider)
}

// Provider returns one model connection.
func (s *Store) Provider(id string) (Provider, error) {
	return scanProvider(s.db.QueryRow(`SELECT `+providerCols+` FROM model_providers WHERE id = ?`, id))
}

// PutProvider stores a model connection.
func (s *Store) PutProvider(p Provider) error {
	if p.ID == "" {
		p.ID = doc.NewRowID()
	}
	models, _ := json.Marshal(p.Models)
	if p.Models == nil {
		models = []byte("[]")
	}
	var tools any
	if p.SupportsTools != nil {
		tools = *p.SupportsTools
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now()
	}
	if p.UpdatedAt.IsZero() {
		p.UpdatedAt = time.Now()
	}
	_, err := s.db.Exec(`INSERT INTO model_providers (`+providerCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET preset = excluded.preset, name = excluded.name, base_url = excluded.base_url,
		auth_kind = excluded.auth_kind, default_model = excluded.default_model, is_local = excluded.is_local,
		supports_tools = excluded.supports_tools, models = excluded.models, updated_at = excluded.updated_at`,
		p.ID, p.Preset, p.Name, p.BaseURL, p.AuthKind, p.DefaultModel, p.IsLocal, tools, string(models), ms(p.CreatedAt), ms(p.UpdatedAt))
	if err == nil {
		s.Notify("model_providers", "")
	}
	return err
}

// DeleteProvider removes a model connection from this device.
func (s *Store) DeleteProvider(id string) error {
	if _, err := s.db.Exec(`DELETE FROM model_providers WHERE id = ?`, id); err != nil {
		return err
	}
	s.Notify("model_providers", "")
	return nil
}
