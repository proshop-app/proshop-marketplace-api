package catalog

import (
	"reflect"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

// Revalidate пересчитывает публикацию, сохраняя исходную карточку для следующей версии схемы.
func (p *Product) Revalidate(definition taxonomy.Definition, now time.Time) bool {
	if p.state.Deleted {
		return false
	}
	publication := definition.Check(p.state.Card.Marketplace)
	if reflect.DeepEqual(publication, p.state.Card.Publication) {
		return false
	}
	p.state.Card.Publication = publication
	p.state.Version = p.originVersion + 1
	p.state.UpdatedAt = now
	return true
}
