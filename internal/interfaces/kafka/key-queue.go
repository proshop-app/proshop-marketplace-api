package kafka

import (
	"container/list"

	kafkago "github.com/segmentio/kafka-go"
)

// Очередь принадлежит одному циклу; ход чередуется между магазинами, затем между их ключами.
type keyQueue struct {
	keys    map[orderingKey]*keyMessages
	tenants map[orderingKey]*tenantKeys
	ready   list.List
	last    orderingKey
	served  bool
}

type tenantKeys struct {
	key      orderingKey
	ready    []*keyMessages
	registry *keyMessages
	element  *list.Element
	keys     int
	active   int
}

type keyMessages struct {
	items  []*delivery
	tenant *tenantKeys
}

type delivery struct {
	message kafkago.Message
	key     orderingKey
	offset  *offsetSlot
}

func newKeyQueue() *keyQueue {
	return &keyQueue{
		keys:    make(map[orderingKey]*keyMessages),
		tenants: make(map[orderingKey]*tenantKeys),
	}
}

func (q *keyQueue) add(item *delivery) {
	if key := q.keys[item.key]; key != nil {
		key.items = append(key.items, item)
		return
	}
	group := item.key.group()
	tenant := q.tenants[group]
	if tenant == nil {
		tenant = &tenantKeys{key: group}
		q.tenants[group] = tenant
	}
	key := &keyMessages{tenant: tenant, items: []*delivery{item}}
	tenant.keys++
	q.keys[item.key] = key
	q.enqueue(key)
}

func (q *keyQueue) enqueue(key *keyMessages) {
	tenant := key.tenant
	if key.items[0].key.kind == orderTenant {
		tenant.registry = key
	} else {
		tenant.ready = append(tenant.ready, key)
	}
	q.schedule(tenant)
}

func (q *keyQueue) schedule(tenant *tenantKeys) {
	if !tenant.runnable() {
		if tenant.element != nil {
			q.ready.Remove(tenant.element)
			tenant.element = nil
		}
		return
	}
	if tenant.element != nil {
		return
	}
	// При единственном шумном магазине новый получает ближайший свободный ход.
	if q.ready.Len() == 1 && (!q.served || q.ready.Front().Value.(*tenantKeys).key == q.last) {
		tenant.element = q.ready.PushFront(tenant)
	} else {
		tenant.element = q.ready.PushBack(tenant)
	}
}

func (t *tenantKeys) runnable() bool {
	if t.registry != nil {
		// Реестр ждёт COMMIT уже запущенных сообщений, затем обрабатывается один.
		return t.active == 0
	}
	return len(t.ready) > 0
}

func (q *keyQueue) next() *delivery {
	if q.ready.Len() == 0 {
		return nil
	}
	tenant := q.ready.Front().Value.(*tenantKeys)
	if tenant.registry != nil {
		return tenant.registry.items[0]
	}
	return tenant.ready[0].items[0]
}

func (q *keyQueue) started() {
	tenant := q.ready.Front().Value.(*tenantKeys)
	q.last, q.served = tenant.key, true
	q.ready.Remove(tenant.element)
	tenant.element = nil
	if tenant.registry == nil {
		tenant.ready[0] = nil
		tenant.ready = tenant.ready[1:]
	}
	tenant.active++
	q.schedule(tenant)
}

func (q *keyQueue) finished(item *delivery) {
	key := q.keys[item.key]
	tenant := key.tenant
	tenant.active--
	key.items[0] = nil
	key.items = key.items[1:]
	if len(key.items) > 0 {
		q.enqueue(key)
		return
	}
	if item.key.kind == orderTenant {
		tenant.registry = nil
	}
	delete(q.keys, item.key)
	tenant.keys--
	if tenant.keys == 0 {
		delete(q.tenants, tenant.key)
	}
	q.schedule(tenant)
}
