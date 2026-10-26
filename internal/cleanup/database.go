package cleanup

import (
	"context"
	"database/sql"
	"fmt"
)

func Vacuum(ctx context.Context, db *sql.DB, dry bool) Step {
	s := Step{Name: "database"}
	var pages, size int64
	if err := db.QueryRowContext(ctx, "PRAGMA freelist_count").Scan(&pages); err != nil {
		s.Error(err)
		return s
	}
	if err := db.QueryRowContext(ctx, "PRAGMA page_size").Scan(&size); err != nil {
		s.Error(err)
		return s
	}
	if pages*size <= 300*1024*1024 {
		s.Skipped++
		return s
	}
	s.Candidates = 1
	if dry {
		s.Bytes = pages * size
		return s
	}
	checkpoint := func() error {
		var busy, log, done int
		err := db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &log, &done)
		if err == nil && busy != 0 {
			return fmt.Errorf("database checkpoint busy")
		}
		return err
	}
	if err := checkpoint(); err != nil {
		s.Error(err)
		return s
	}
	if _, err := db.ExecContext(ctx, "VACUUM"); err != nil {
		s.Error(err)
		return s
	}
	s.Deleted = 1
	s.Bytes = pages * size
	s.Error(checkpoint())
	return s
}
