package fake

import (
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// ItemCheckCall сохраняет проверенный аккаунт каждого обращения к продавцу.
type ItemCheckCall struct {
	TenantID ids.TenantID
	Request  ports.GroupPricingRequest
}

// ItemCheckCalls возвращает копию истории для проверки границы аккаунта.
func (p *Platform) ItemCheckCalls() []ItemCheckCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]ItemCheckCall(nil), p.checks...)
}
