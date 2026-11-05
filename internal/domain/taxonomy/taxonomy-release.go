package taxonomy

import (
	"encoding/hex"
	"strings"

	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

const (
	sha256Size = 32
	// MaxTaxonomyVersion сохраняет точность версии у JSON-клиентов JavaScript.
	MaxTaxonomyVersion int64 = 9007199254740991
)

var (
	// ErrInvalidRelease — поставка не прошла проверку до обращения к хранилищу.
	ErrInvalidRelease = errs.New(errs.Invalid, "taxonomy.invalid_release", "некорректная поставка каталога")
	// ErrReleaseConflict — автоматическая замена не имеет подтверждённой исходной версии.
	ErrReleaseConflict = errs.New(
		errs.Conflict,
		"taxonomy.release_conflict",
		"поставка устарела, изменена или конфликтует с опубликованным каталогом",
	)
)

// Release — неизменяемая поставка; её номер независим от версии публикации.
type Release struct {
	Version    int64
	Checksum   string
	Categories []Category
	Attributes []Attribute
	Schemas    map[string]Schema
	// ExpectedVersion разрешает явное обновление проверенного операторского снимка.
	ExpectedVersion int64
}

// ReleaseState — последняя операция поставки, записанная вместе со снимком.
type ReleaseState struct {
	Version           int64
	Checksum          string
	PublishedVersion  int64
	RolledBackVersion int64
}

// ReleaseRecord хранит исходный снимок для атомарного отката поставки.
type ReleaseRecord struct {
	ReleaseState
	Previous Definition
}

// ReleaseResult сообщает фактическую версию без повторной публикации при retry.
type ReleaseResult struct {
	Release         int64 `json:"release"`
	SnapshotVersion int64 `json:"snapshotVersion"`
	Changed         bool  `json:"changed"`
	RolledBack      bool  `json:"rolledBack"`
}

// ValidateRelease не допускает пустую или повреждённую готовую поставку.
func ValidateRelease(release Release) error {
	checksum, err := hex.DecodeString(release.Checksum)
	if release.Version < 1 || release.Version > MaxTaxonomyVersion || err != nil || len(checksum) != sha256Size ||
		len(release.Categories) == 0 || release.Checksum != strings.ToLower(release.Checksum) ||
		release.ExpectedVersion < 0 || release.ExpectedVersion > MaxTaxonomyVersion {
		return ErrInvalidRelease
	}

	return release.Definition().Validate()
}

// PlanRelease защищает операторские правки и не позволяет понизить поставку.
func PlanRelease(
	current Definition,
	latest *ReleaseState,
	release Release,
) (bool, error) {
	if latest == nil {
		initial := current.Version == 0 && len(current.Categories) == 0
		if (!initial && release.ExpectedVersion != current.Version) || current.Version >= MaxTaxonomyVersion ||
			(release.ExpectedVersion != 0 && release.ExpectedVersion != current.Version) {
			return false, ErrReleaseConflict
		}

		return true, nil
	}
	if release.Version < latest.Version || (release.Version == latest.Version &&
		(release.Checksum != latest.Checksum || latest.RolledBackVersion != 0)) {
		return false, ErrReleaseConflict
	}
	if release.Version == latest.Version {
		return false, nil
	}

	expected := latest.PublishedVersion
	if latest.RolledBackVersion != 0 {
		expected = latest.RolledBackVersion
	}
	if (current.Version != expected && release.ExpectedVersion != current.Version) ||
		current.Version >= MaxTaxonomyVersion ||
		(release.ExpectedVersion != 0 && release.ExpectedVersion != current.Version) {
		return false, ErrReleaseConflict
	}

	return true, nil
}

// PlanTaxonomyRollback откатывает только последнюю поставку без последующих правок.
func PlanTaxonomyRollback(
	current Definition,
	latest *ReleaseState,
	release Release,
) (bool, error) {
	if latest == nil || latest.Version != release.Version || latest.Checksum != release.Checksum {
		return false, ErrReleaseConflict
	}
	if latest.RolledBackVersion != 0 {
		return false, nil
	}
	if current.Version != latest.PublishedVersion || current.Version >= MaxTaxonomyVersion {
		return false, ErrReleaseConflict
	}

	return true, nil
}

// Definition отделяет номер поставки от номера опубликованного снимка.
func (r Release) Definition() Definition {
	return Definition{
		Version:    r.Version,
		Attributes: r.Attributes,
		Schemas:    r.Schemas,
		Categories: r.Categories,
	}
}
