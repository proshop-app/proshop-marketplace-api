// Package kafka — обработчики сообщений из топиков установки: разбор,
// определение магазина и вызов сценариев приложения.
package kafka

// Топики установки без префикса. Имена — как в сервере
// (src/common/enums/broker-topics.ts); события реестра платформы сервер
// заводит в своём PR-08d.
const (
	TopicProductProjected    = "catalog.product.projected"
	TopicTagProjected        = "catalog.tag.projected"
	TopicStockItemUpdated    = "stock.item.updated"
	TopicProductPriceUpdated = "catalog.product.price-updated"
	TopicTenantCreated       = "platform.tenant.created"
	TopicTenantUpdated       = "platform.tenant.updated"
	TopicTenantSuspended     = "platform.tenant.suspended"
	TopicTenantResumed       = "platform.tenant.resumed"
	TopicTenantDeleted       = "platform.tenant.deleted"
)

// HeaderTenant — заголовок с магазином, который ставит продюсер сервера.
const HeaderTenant = "x-tenant"
