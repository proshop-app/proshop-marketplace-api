package catalog

import (
	"strings"
	"time"

	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// TagSnapshot — снимок тега из события проекции.
type TagSnapshot struct {
	TenantID ids.TenantID
	TagID    ids.TagID
	Revision int64
	Deleted  bool
	Value    string
}

// TagState — плоский снимок тега.
type TagState struct {
	TenantID  ids.TenantID
	TagID     ids.TagID
	Revision  int64
	Deleted   bool
	Value     string
	Version   int64
	UpdatedAt time.Time
}

// Tag — тег справочника магазина.
type Tag struct {
	state         TagState
	originVersion int64
}

// TagLabel — тег карточки с подписью.
type TagLabel struct {
	ID    string
	Label string
}

// NewTag заводит тег из первого снимка.
func NewTag(snapshot TagSnapshot, now time.Time) (*Tag, error) {
	if err := snapshot.validate(); err != nil {
		return nil, err
	}

	tag := &Tag{}
	tag.assign(snapshot, now)

	return tag, nil
}

// RestoreTag поднимает тег из хранилища.
func RestoreTag(state TagState) *Tag {
	return &Tag{state: state, originVersion: state.Version}
}

// Apply применяет снимок с ревизией больше текущей.
func (t *Tag) Apply(snapshot TagSnapshot, now time.Time) (bool, error) {
	if err := snapshot.validate(); err != nil {
		return false, err
	}
	if snapshot.TenantID != t.state.TenantID || snapshot.TagID != t.state.TagID {
		return false, ErrInvalidSnapshot.WithMessage("снимок другого тега")
	}
	if snapshot.Revision <= t.state.Revision {
		return false, nil
	}

	t.assign(snapshot, now)

	return true, nil
}

func (t *Tag) assign(snapshot TagSnapshot, now time.Time) {
	t.state.TenantID = snapshot.TenantID
	t.state.TagID = snapshot.TagID
	t.state.Revision = snapshot.Revision
	t.state.Deleted = snapshot.Deleted
	t.state.Value = ""
	if !snapshot.Deleted {
		t.state.Value = strings.TrimSpace(snapshot.Value)
	}
	t.state.Version = t.originVersion + 1
	t.state.UpdatedAt = now
}

// State — снимок для хранения.
func (t *Tag) State() TagState { return t.state }

// OriginVersion — версия на момент чтения; у нового тега — ноль.
func (t *Tag) OriginVersion() int64 { return t.originVersion }

// MarkSaved фиксирует сохранённую версию.
func (t *Tag) MarkSaved() { t.originVersion = t.state.Version }

func (s TagSnapshot) validate() error {
	switch {
	case s.Revision < 1:
		return ErrInvalidSnapshot.WithMessage("ревизия тега должна быть больше нуля")
	case !s.Deleted && strings.TrimSpace(s.Value) == "":
		return ErrInvalidSnapshot.WithMessage("у тега нет значения")
	default:
		return nil
	}
}

// LabelTags подписывает теги карточки по справочнику магазина; тег без
// записи в справочнике подписывается своим значением.
func LabelTags(tags []string, dictionary map[string]string) []TagLabel {
	labels := make([]TagLabel, 0, len(tags))
	for _, tag := range tags {
		label := tag
		if value, ok := dictionary[tag]; ok && value != "" {
			label = value
		}

		labels = append(labels, TagLabel{ID: tag, Label: label})
	}

	return labels
}
