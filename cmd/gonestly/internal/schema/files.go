package schema

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// SnapshotFile is the file in the migrations directory that remembers the schema that the
// migrations made, so that the next change is only the difference with it.
const SnapshotFile = "schema.snapshot.json"

// ErrNoSnapshot is returned when migrations exist that this tool did not write, so it does not
// know the schema they made.
var ErrNoSnapshot = errors.New("the migrations were not made from the models, so the schema is not known")

// LoadSnapshot reads the schema that the migrations in dir made. A directory without migrations
// has the empty schema; one with migrations but without a snapshot returns ErrNoSnapshot.
func LoadSnapshot(dir string) (Schema, error) {
	data, err := os.ReadFile(filepath.Join(dir, SnapshotFile))
	switch {
	case err == nil:
		return Unmarshal(data)
	case !errors.Is(err, os.ErrNotExist):
		return Schema{}, err
	}
	if hasMigrations(dir) {
		return Schema{}, ErrNoSnapshot
	}
	return Schema{Version: snapshotVersion}, nil
}

func hasMigrations(dir string) bool {
	matches, _ := filepath.Glob(filepath.Join(dir, "*.sql"))
	return len(matches) > 0
}

// SaveSnapshot writes the schema that the migrations in dir make.
func SaveSnapshot(dir string, s Schema) error {
	data, err := Marshal(s)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, SnapshotFile), data, 0o644) //nolint:gosec // the snapshot is meant to be committed and read
}

// Written lists the files of a migration.
type Written struct{ Up, Down string }

// WriteMigration writes the pair of files of a migration.
func WriteMigration(dir, version, name, up, down string) (Written, error) {
	w := Written{
		Up:   filepath.Join(dir, fmt.Sprintf("%s_%s.up.sql", version, name)),
		Down: filepath.Join(dir, fmt.Sprintf("%s_%s.down.sql", version, name)),
	}
	for path, content := range map[string]string{w.Up: up, w.Down: down} {
		if _, err := os.Stat(path); err == nil {
			return Written{}, fmt.Errorf("%s exists already", path)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil { //nolint:gosec // migrations are meant to be readable
			return Written{}, err
		}
	}
	return w, nil
}

// NextVersion returns the version for a new migration in dir: the next counter for 0001 style
// versions, or a timestamp that is later than every version that exists.
func NextVersion(dir string, now time.Time) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", dir, err)
	}
	var highest, latestStamp int64
	counter := false
	for _, e := range entries {
		prefix, _, ok := strings.Cut(e.Name(), "_")
		if !ok || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		var n int64
		if _, err := fmt.Sscan(prefix, &n); err != nil {
			continue
		}
		counter = len(prefix) < 14
		highest = max(highest, n)
		if len(prefix) == 14 {
			latestStamp = max(latestStamp, n)
		}
	}
	if counter {
		return fmt.Sprintf("%04d", highest+1), nil
	}
	const layout = "20060102150405"
	version := now.UTC().Format(layout)
	var taken int64
	_, _ = fmt.Sscan(version, &taken)
	if latestStamp >= taken {
		latest, err := time.Parse(layout, strconv.FormatInt(latestStamp, 10))
		if err != nil {
			return "", fmt.Errorf("the migration version %d is not a timestamp", latestStamp)
		}
		version = latest.Add(time.Second).Format(layout)
	}
	return version, nil
}
