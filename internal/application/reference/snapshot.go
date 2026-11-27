package reference

import (
	"context"
	"encoding/base64"
	"encoding/json"

	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	// SnapshotSchemaVersion отмечает постраничный контракт снимка.
	SnapshotSchemaVersion = 2
	snapshotPageSize      = 128
	snapshotCursorLimit   = 512
)

type snapshotCursor struct {
	Dictionary string
	Revision   int64
	Since      *int64
	After      int64
}

// Snapshot читает ограниченную страницу неизменяемой ревизии словаря.
func (s *Service) Snapshot(ctx context.Context, code string, since *int64, cursor string) (Snapshot, error) {
	position, err := parseSnapshotCursor(code, since, cursor)
	if err != nil {
		return Snapshot{}, err
	}
	var result Snapshot
	err = s.read(ctx, code, func(ctx context.Context, repo reference.Repository, dict *reference.Dictionary) error {
		var err error
		result, err = s.snapshotPage(ctx, repo, dict, position)
		return err
	})
	return result, err
}

func (s *Service) snapshotPage(
	ctx context.Context,
	repo reference.Repository,
	dict *reference.Dictionary,
	position snapshotCursor,
) (Snapshot, error) {
	state := dict.State()
	revision := state.Revision
	if position.Revision > revision {
		return Snapshot{}, reference.ErrInvalid
	}
	if position.After == 0 {
		position.Revision = revision
		if position.Since != nil {
			if err := s.resolveSnapshotMode(ctx, repo, &position); err != nil {
				return Snapshot{}, err
			}
		}
	}
	result := Snapshot{
		Kind:       state.Kind,
		Dictionary: dict.Code(),
		Revision:   position.Revision,
		Normalizer: dict.Normalizer(),
		Full:       position.Since == nil,
		Entries:    []reference.EntryState{},
		Removed:    []ids.UUID{},
	}
	if position.Revision == 0 || (position.Since != nil && *position.Since == position.Revision) {
		return result, nil
	}
	// Счётчик соответствует прочитанной голове, исторические страницы идут по закреплённой ревизии.
	if result.Full && position.Revision == revision && state.Entries == 0 {
		return result, nil
	}
	rows, err := loadSnapshotRows(ctx, repo, position)
	if err != nil {
		return Snapshot{}, err
	}
	if len(rows) > snapshotPageSize {
		rows = rows[:snapshotPageSize]
		position.After = rows[len(rows)-1].Position
		result.NextCursor = encodeSnapshotCursor(position)
	}
	for _, row := range rows {
		if row.State == nil {
			result.Removed = append(result.Removed, row.ID)
			continue
		}
		result.Entries = append(result.Entries, *row.State)
	}
	return result, nil
}

func (s *Service) resolveSnapshotMode(
	ctx context.Context,
	repo reference.Repository,
	position *snapshotCursor,
) error {
	switch {
	case *position.Since > position.Revision:
		position.Since = nil
	case *position.Since < position.Revision:
		before := s.clock.Now().Add(-s.config.Horizon)
		expired, err := repo.SnapshotSinceExpired(ctx, position.Dictionary, *position.Since, position.Revision, before)
		if err != nil {
			return err
		}
		if expired {
			position.Since = nil
		}
	}
	return nil
}

func loadSnapshotRows(
	ctx context.Context,
	repo reference.Repository,
	position snapshotCursor,
) ([]reference.SnapshotRow, error) {
	if position.Since == nil {
		return repo.SnapshotEntries(ctx, position.Dictionary, position.Revision, position.After, snapshotPageSize+1)
	}
	return repo.SnapshotChanges(
		ctx,
		position.Dictionary,
		*position.Since,
		position.Revision,
		position.After,
		snapshotPageSize+1,
	)
}

func parseSnapshotCursor(code string, since *int64, token string) (snapshotCursor, error) {
	if token == "" {
		if since != nil && *since < 0 {
			return snapshotCursor{}, reference.ErrInvalid
		}
		return snapshotCursor{Dictionary: code, Since: since}, nil
	}
	if since != nil || len(token) > snapshotCursorLimit {
		return snapshotCursor{}, reference.ErrInvalid
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return snapshotCursor{}, reference.ErrInvalid
	}
	var position snapshotCursor
	if json.Unmarshal(raw, &position) != nil || position.Dictionary != code || position.Revision < 1 {
		return snapshotCursor{}, reference.ErrInvalid
	}
	if position.After < 1 || (position.Since != nil && (*position.Since < 0 || *position.Since >= position.Revision)) {
		return snapshotCursor{}, reference.ErrInvalid
	}
	return position, nil
}

func encodeSnapshotCursor(position snapshotCursor) string {
	raw, _ := json.Marshal(position)
	return base64.RawURLEncoding.EncodeToString(raw)
}
