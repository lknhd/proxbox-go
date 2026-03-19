package db

import (
	"database/sql"
	"fmt"

	"github.com/lknhd/proxbox-go/internal/models"
	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS users (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    username TEXT NOT NULL DEFAULT '',
    fingerprint TEXT UNIQUE NOT NULL,
    public_key TEXT NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS containers (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL REFERENCES users(id),
    name TEXT NOT NULL,
    vmid INTEGER UNIQUE NOT NULL,
    size TEXT NOT NULL DEFAULT 'small',
    status TEXT NOT NULL DEFAULT 'stopped',
    ip_address TEXT DEFAULT '',
    has_snapshot INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(user_id, name)
);
`

type DB struct {
	conn *sql.DB
}

func Open(path string) (*DB, error) {
	conn, err := sql.Open("sqlite", path+"?_journal_mode=WAL")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	if _, err := conn.Exec(schema); err != nil {
		return nil, fmt.Errorf("init schema: %w", err)
	}

	return &DB{conn: conn}, nil
}

func (d *DB) Close() error {
	return d.conn.Close()
}

func (d *DB) GetOrCreateUser(username, fingerprint, publicKey string) (*models.User, error) {
	var user models.User
	err := d.conn.QueryRow(
		"SELECT id, username, fingerprint, public_key, created_at FROM users WHERE fingerprint = ?",
		fingerprint,
	).Scan(&user.ID, &user.Username, &user.Fingerprint, &user.PublicKey, &user.CreatedAt)

	if err == nil {
		if user.Username != username {
			_, _ = d.conn.Exec("UPDATE users SET username = ? WHERE id = ?", username, user.ID)
			user.Username = username
		}
		return &user, nil
	}
	if err != sql.ErrNoRows {
		return nil, fmt.Errorf("query user: %w", err)
	}

	res, err := d.conn.Exec(
		"INSERT INTO users (username, fingerprint, public_key) VALUES (?, ?, ?)",
		username, fingerprint, publicKey,
	)
	if err != nil {
		return nil, fmt.Errorf("insert user: %w", err)
	}

	id, _ := res.LastInsertId()
	return d.getUserByID(id)
}

func (d *DB) getUserByID(id int64) (*models.User, error) {
	var user models.User
	err := d.conn.QueryRow(
		"SELECT id, username, fingerprint, public_key, created_at FROM users WHERE id = ?", id,
	).Scan(&user.ID, &user.Username, &user.Fingerprint, &user.PublicKey, &user.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func (d *DB) CreateContainer(userID int64, name string, vmid int, size string) (*models.Container, error) {
	res, err := d.conn.Exec(
		"INSERT INTO containers (user_id, name, vmid, size) VALUES (?, ?, ?, ?)",
		userID, name, vmid, size,
	)
	if err != nil {
		return nil, fmt.Errorf("insert container: %w", err)
	}

	id, _ := res.LastInsertId()
	return d.getContainerByID(id)
}

func (d *DB) GetContainer(userID int64, name string) (*models.Container, error) {
	return d.scanContainer(
		d.conn.QueryRow(
			"SELECT id, user_id, name, vmid, size, status, ip_address, has_snapshot, created_at FROM containers WHERE user_id = ? AND name = ?",
			userID, name,
		),
	)
}

func (d *DB) GetContainersForUser(userID int64) ([]*models.Container, error) {
	rows, err := d.conn.Query(
		"SELECT id, user_id, name, vmid, size, status, ip_address, has_snapshot, created_at FROM containers WHERE user_id = ? ORDER BY name",
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var containers []*models.Container
	for rows.Next() {
		c, err := d.scanContainerRow(rows)
		if err != nil {
			return nil, err
		}
		containers = append(containers, c)
	}
	return containers, rows.Err()
}

func (d *DB) UpdateContainer(id int64, status string, ipAddress string, hasSnapshot *bool) error {
	query := "UPDATE containers SET status = ?, ip_address = ?"
	args := []any{status, ipAddress}

	if hasSnapshot != nil {
		query += ", has_snapshot = ?"
		if *hasSnapshot {
			args = append(args, 1)
		} else {
			args = append(args, 0)
		}
	}

	query += " WHERE id = ?"
	args = append(args, id)

	_, err := d.conn.Exec(query, args...)
	return err
}

func (d *DB) DeleteContainer(id int64) error {
	_, err := d.conn.Exec("DELETE FROM containers WHERE id = ?", id)
	return err
}

func (d *DB) NextAvailableVMID(start, end int) (int, error) {
	rows, err := d.conn.Query("SELECT vmid FROM containers ORDER BY vmid")
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	used := make(map[int]bool)
	for rows.Next() {
		var vmid int
		if err := rows.Scan(&vmid); err != nil {
			return 0, err
		}
		used[vmid] = true
	}

	for vmid := start; vmid <= end; vmid++ {
		if !used[vmid] {
			return vmid, nil
		}
	}
	return 0, fmt.Errorf("no available VMIDs in range %d-%d", start, end)
}

func (d *DB) getContainerByID(id int64) (*models.Container, error) {
	return d.scanContainer(
		d.conn.QueryRow(
			"SELECT id, user_id, name, vmid, size, status, ip_address, has_snapshot, created_at FROM containers WHERE id = ?",
			id,
		),
	)
}

func (d *DB) scanContainer(row *sql.Row) (*models.Container, error) {
	var c models.Container
	var hasSnap int
	err := row.Scan(&c.ID, &c.UserID, &c.Name, &c.VMID, &c.Size, &c.Status, &c.IPAddress, &hasSnap, &c.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	c.HasSnapshot = hasSnap != 0
	return &c, nil
}

func (d *DB) scanContainerRow(rows *sql.Rows) (*models.Container, error) {
	var c models.Container
	var hasSnap int
	err := rows.Scan(&c.ID, &c.UserID, &c.Name, &c.VMID, &c.Size, &c.Status, &c.IPAddress, &hasSnap, &c.CreatedAt)
	if err != nil {
		return nil, err
	}
	c.HasSnapshot = hasSnap != 0
	return &c, nil
}
