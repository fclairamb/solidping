package sqlite

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
)

// memTemplate holds, per process, the image of an in-memory database taken
// right after its first successful Initialize: the fully migrated schema,
// bun_migrations and the guard's checksums, and no other row.
//
// Every in-memory database starts empty, and replaying the ~217 KB of
// migrations into it is by far the most expensive thing a Go test does:
// 68% of internal/handlers/auth's CPU on 2026-10-11, where most tests open
// their own database. Restoring this image yields the same database as
// running the migrations, without the parsing. Production opens one database
// per process, so there the template is built once and never read.
//
//nolint:gochecknoglobals // process-wide cache shared by every in-memory Service
var memTemplate struct {
	sync.Mutex

	image []byte
	// disabled is set once the linked driver turns out not to support
	// serialization, so the remaining databases skip straight to migrating.
	disabled bool
}

// memTemplateRestores counts databases initialized from memTemplate, so a
// test can tell a restore from a full migration.
//
//nolint:gochecknoglobals // test-observable counter for the process-wide cache
var memTemplateRestores atomic.Int64

// errSerializeUnsupported means the linked driver cannot (de)serialize.
var errSerializeUnsupported = errors.New("sqlite driver does not support serialization")

// modernSerializer is modernc.org/sqlite's driver connection.
type modernSerializer interface {
	Serialize() ([]byte, error)
	Deserialize(buf []byte) error
}

// mattnSerializer is mattn/go-sqlite3's driver connection.
type mattnSerializer interface {
	Serialize(schema string) ([]byte, error)
	Deserialize(buf []byte, schema string) error
}

// initializeInMemory restores the migrated image into a fresh in-memory
// database, building that image on the first call. Anything unexpected falls
// back to running the migrations: the template is an optimization and must
// never change what Initialize produces.
func (s *Service) initializeInMemory(ctx context.Context) error {
	fresh, err := s.isEmpty(ctx)
	if err != nil || !fresh {
		// A database that already holds tables (a test re-initializing, or
		// seeding rows before migrating) must go through the real path.
		return s.migrate(ctx)
	}

	// Held while the first image is built, so the tests that start at the
	// same moment wait for it instead of each replaying the migrations.
	memTemplate.Lock()
	defer memTemplate.Unlock()

	if memTemplate.disabled {
		return s.migrate(ctx)
	}

	if memTemplate.image == nil {
		if migrateErr := s.migrate(ctx); migrateErr != nil {
			return migrateErr
		}

		image, serializeErr := s.serialize(ctx)
		if serializeErr != nil {
			memTemplate.disabled = true

			// The database is migrated; only the cache is unavailable.
			return nil
		}

		memTemplate.image = image

		return nil
	}

	if restoreErr := s.deserialize(ctx, memTemplate.image); restoreErr != nil {
		return s.migrate(ctx)
	}

	memTemplateRestores.Add(1)

	// Same guard pass the migration path ends with, so a mode that refuses a
	// checksum mismatch behaves identically on a restored database.
	guard, err := newMigrationGuard(s.db)
	if err != nil {
		return err
	}

	mismatches, err := guard.Reconcile(ctx, s.guardMode)
	if err != nil {
		return err
	}

	return s.guardMode.Apply(mismatches)
}

// isEmpty reports whether the database has no schema object at all.
func (s *Service) isEmpty(ctx context.Context) (bool, error) {
	var count int
	if err := s.db.NewRaw("SELECT count(*) FROM sqlite_master").Scan(ctx, &count); err != nil {
		return false, fmt.Errorf("failed to inspect sqlite_master: %w", err)
	}

	return count == 0, nil
}

// serialize returns the image of the main database.
func (s *Service) serialize(ctx context.Context) ([]byte, error) {
	var image []byte

	err := s.rawConn(ctx, func(driverConn any) error {
		var serializeErr error

		switch c := driverConn.(type) {
		case modernSerializer:
			image, serializeErr = c.Serialize()
		case mattnSerializer:
			image, serializeErr = c.Serialize("main")
		default:
			return errSerializeUnsupported
		}

		if serializeErr == nil && len(image) == 0 {
			serializeErr = errSerializeUnsupported
		}

		return serializeErr
	})

	return image, err
}

// deserialize replaces the main database with image.
func (s *Service) deserialize(ctx context.Context, image []byte) error {
	return s.rawConn(ctx, func(driverConn any) error {
		switch c := driverConn.(type) {
		case modernSerializer:
			return c.Deserialize(image)
		case mattnSerializer:
			return c.Deserialize(image, "main")
		default:
			return errSerializeUnsupported
		}
	})
}

// rawConn runs callback on the driver connection. An in-memory database is pinned
// to one connection (each :memory: connection is a separate database), so
// this is the connection every later query uses.
func (s *Service) rawConn(ctx context.Context, callback func(driverConn any) error) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("failed to get sqlite connection: %w", err)
	}

	defer func() { _ = conn.Close() }()

	if rawErr := conn.Raw(callback); rawErr != nil {
		return fmt.Errorf("sqlite raw connection: %w", rawErr)
	}

	return nil
}
