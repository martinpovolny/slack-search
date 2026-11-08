package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"time"
)

type schemaMigration struct {
	version int
	name    string
	up      func(*sql.Conn) (int64, error)
}

var schemaMigrations = []schemaMigration{
	{
		version: 1,
		name:    "deduplicate-block-kit-message-text",
		up:      migrateDuplicateBlockKitMessageText,
	},
}

func runMigrations(db *sql.DB) error {
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("get migration connection: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("begin migration transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(ctx, "ROLLBACK")
		}
	}()

	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
		rows_affected INTEGER NOT NULL DEFAULT 0
	)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	type appliedMigration struct {
		migration    schemaMigration
		rowsAffected int64
		duration     time.Duration
	}
	var appliedMigrations []appliedMigration

	for _, migration := range schemaMigrations {
		var applied int
		err := conn.QueryRowContext(ctx, "SELECT 1 FROM schema_migrations WHERE version=?", migration.version).Scan(&applied)
		if err == nil {
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("check migration %d (%s): %w", migration.version, migration.name, err)
		}

		started := time.Now()
		log.Printf("Applying database migration %d (%s); this may take a few minutes for a large archive", migration.version, migration.name)
		rowsAffected, err := migration.up(conn)
		if err != nil {
			return fmt.Errorf("apply migration %d (%s): %w", migration.version, migration.name, err)
		}
		if _, err := conn.ExecContext(ctx,
			"INSERT INTO schema_migrations(version, name, rows_affected) VALUES (?, ?, ?)",
			migration.version, migration.name, rowsAffected,
		); err != nil {
			return fmt.Errorf("record migration %d (%s): %w", migration.version, migration.name, err)
		}
		appliedMigrations = append(appliedMigrations, appliedMigration{
			migration:    migration,
			rowsAffected: rowsAffected,
			duration:     time.Since(started),
		})
	}

	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("commit database migrations: %w", err)
	}
	committed = true
	for _, applied := range appliedMigrations {
		log.Printf("Applied database migration %d (%s): affected %d row(s) in %s",
			applied.migration.version,
			applied.migration.name,
			applied.rowsAffected,
			applied.duration.Round(time.Second),
		)
	}
	return nil
}

// The nested CASE expressions make SQLite evaluate the JSON tree only for rows
// whose indexed text is exactly the top-level Slack text repeated twice.
const duplicatedBlockKitTextPredicate = `CASE
	WHEN instr(messages.text, char(10)) > 0 THEN
		CASE WHEN json_valid(messages.raw_json) = 1 THEN
			CASE WHEN json_type(messages.raw_json, '$.text') = 'text' THEN
				CASE WHEN messages.text = json_extract(messages.raw_json, '$.text')
					|| char(10) || json_extract(messages.raw_json, '$.text') THEN
					EXISTS (
						SELECT 1
						FROM json_tree(messages.raw_json, '$.blocks') AS block
						WHERE block.key = 'text'
						  AND block.value = json_extract(messages.raw_json, '$.text')
					)
				ELSE 0 END
			ELSE 0 END
		ELSE 0 END
	ELSE 0 END`

func migrateDuplicateBlockKitMessageText(conn *sql.Conn) (int64, error) {
	result, err := conn.ExecContext(context.Background(),
		"UPDATE messages SET text=json_extract(raw_json, '$.text') WHERE "+duplicatedBlockKitTextPredicate,
	)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
