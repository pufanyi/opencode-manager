package store

import (
	"database/sql"
	"fmt"
	"time"
)

type Instance struct {
	ID           string
	Name         string
	Directory    string
	Port         int
	Password     string
	Status       string
	AutoStart    bool
	ProviderType string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

const instanceCols = `id, name, directory, port, password, status, auto_start, provider_type, created_at, updated_at`

func (s *Store) CreateInstance(inst *Instance) error {
	if inst.ProviderType == "" {
		inst.ProviderType = "claudecode"
	}
	_, err := s.db.Exec(
		`INSERT INTO instances (id, name, directory, port, password, status, auto_start, provider_type)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		inst.ID, inst.Name, inst.Directory, inst.Port, inst.Password, inst.Status, inst.AutoStart, inst.ProviderType,
	)
	if err != nil {
		return fmt.Errorf("creating instance: %w", err)
	}
	return nil
}

func (s *Store) GetInstance(id string) (*Instance, error) {
	return s.scanInstance(s.db.QueryRow(
		`SELECT `+instanceCols+` FROM instances WHERE id = ?`, id,
	))
}

func (s *Store) GetInstanceByName(name string) (*Instance, error) {
	return s.scanInstance(s.db.QueryRow(
		`SELECT `+instanceCols+` FROM instances WHERE name = ?`, name,
	))
}

func (s *Store) ListInstances() ([]*Instance, error) {
	rows, err := s.db.Query(
		`SELECT ` + instanceCols + ` FROM instances ORDER BY name`,
	)
	if err != nil {
		return nil, fmt.Errorf("listing instances: %w", err)
	}
	defer rows.Close()

	var instances []*Instance
	for rows.Next() {
		inst, err := s.scanInstanceRow(rows)
		if err != nil {
			return nil, err
		}
		instances = append(instances, inst)
	}
	return instances, rows.Err()
}

func (s *Store) UpdateInstanceStatus(id, status string) error {
	_, err := s.db.Exec(
		`UPDATE instances SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		status, id,
	)
	return err
}

func (s *Store) UpdateInstancePort(id string, port int) error {
	_, err := s.db.Exec(
		`UPDATE instances SET port = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		port, id,
	)
	return err
}

func (s *Store) DeleteInstance(id string) error {
	_, _ = s.db.Exec(`DELETE FROM claude_sessions WHERE instance_id = ?`, id)
	_, err := s.db.Exec(`DELETE FROM instances WHERE id = ?`, id)
	return err
}

func (s *Store) GetRunningInstances() ([]*Instance, error) {
	rows, err := s.db.Query(
		`SELECT `+instanceCols+` FROM instances WHERE status = 'running' OR auto_start = 1`,
	)
	if err != nil {
		return nil, fmt.Errorf("getting running instances: %w", err)
	}
	defer rows.Close()

	var instances []*Instance
	for rows.Next() {
		inst, err := s.scanInstanceRow(rows)
		if err != nil {
			return nil, err
		}
		instances = append(instances, inst)
	}
	return instances, rows.Err()
}

// Claude session methods

type ClaudeSession struct {
	ID         string
	InstanceID string
	Title      string
	CreatedAt  time.Time
}

func (s *Store) CreateClaudeSession(instanceID, sessionID, title string) error {
	_, err := s.db.Exec(
		`INSERT INTO claude_sessions (id, instance_id, title) VALUES (?, ?, ?)`,
		sessionID, instanceID, title,
	)
	return err
}

func (s *Store) GetClaudeSession(sessionID string) (*ClaudeSession, error) {
	cs := &ClaudeSession{}
	err := s.db.QueryRow(
		`SELECT id, instance_id, title, created_at FROM claude_sessions WHERE id = ?`, sessionID,
	).Scan(&cs.ID, &cs.InstanceID, &cs.Title, &cs.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return cs, nil
}

func (s *Store) ListClaudeSessions(instanceID string) ([]ClaudeSession, error) {
	rows, err := s.db.Query(
		`SELECT id, instance_id, title, created_at FROM claude_sessions WHERE instance_id = ? ORDER BY created_at DESC`, instanceID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []ClaudeSession
	for rows.Next() {
		var cs ClaudeSession
		if err := rows.Scan(&cs.ID, &cs.InstanceID, &cs.Title, &cs.CreatedAt); err != nil {
			return nil, err
		}
		sessions = append(sessions, cs)
	}
	return sessions, rows.Err()
}

func (s *Store) scanInstance(row *sql.Row) (*Instance, error) {
	inst := &Instance{}
	err := row.Scan(
		&inst.ID, &inst.Name, &inst.Directory, &inst.Port, &inst.Password,
		&inst.Status, &inst.AutoStart, &inst.ProviderType, &inst.CreatedAt, &inst.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scanning instance: %w", err)
	}
	return inst, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func (s *Store) scanInstanceRow(row rowScanner) (*Instance, error) {
	inst := &Instance{}
	err := row.Scan(
		&inst.ID, &inst.Name, &inst.Directory, &inst.Port, &inst.Password,
		&inst.Status, &inst.AutoStart, &inst.ProviderType, &inst.CreatedAt, &inst.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("scanning instance: %w", err)
	}
	return inst, nil
}
