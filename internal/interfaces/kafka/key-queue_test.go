package kafka

import (
	"fmt"
	"testing"
)

func TestKeyQueueSerializesOneKeyAndSchedulesOthers(t *testing.T) {
	queue := newKeyQueue()
	first := &delivery{key: orderingKey{kind: orderProduct, tenant: "t1", entity: "p1"}}
	second := &delivery{key: first.key}
	other := &delivery{key: orderingKey{kind: orderProduct, tenant: "t1", entity: "p2"}}
	queue.add(first)
	queue.add(second)
	queue.add(other)
	if queue.next() != first {
		t.Fatal("первое сообщение ключа потеряно")
	}
	queue.started()
	if queue.next() != other {
		t.Fatal("занятый ключ задержал другой")
	}
	queue.started()
	if queue.next() != nil {
		t.Fatal("два сообщения ключа выданы одновременно")
	}
	queue.finished(other)
	queue.finished(first)
	if queue.next() != second {
		t.Fatal("следующее сообщение ключа потеряно")
	}
	queue.started()
	queue.finished(second)
	if len(queue.keys) != 0 || queue.next() != nil {
		t.Fatal("очередь удерживает завершённые ключи")
	}
}

func TestKeyQueueGivesSmallTenantTurnWithinWorkersStarts(t *testing.T) {
	for _, workers := range []int{1, 8} {
		queue := newKeyQueue()
		for i := 0; i < 500; i++ {
			queue.add(&delivery{key: orderingKey{kind: orderProduct, tenant: "a", entity: fmt.Sprint(i)}})
		}
		point := &delivery{key: orderingKey{kind: orderProduct, tenant: "b", entity: "p1"}}
		queue.add(point)
		started := false
		for range workers {
			item := queue.next()
			queue.started()
			if item == point {
				started = true
				break
			}
		}
		if !started {
			t.Fatalf("магазин Б ждёт за волной А больше %d запусков", workers)
		}
	}
}

func TestKeyQueueRoundRobinTenantsAndReactivation(t *testing.T) {
	queue := newKeyQueue()
	for _, tenant := range []string{"a", "b"} {
		for i := 0; i < 20; i++ {
			queue.add(&delivery{key: orderingKey{kind: orderProduct, tenant: tenant, entity: fmt.Sprint(i)}})
		}
	}
	previous := ""
	for range 40 {
		item := queue.next()
		if item == nil || item.key.tenant == previous {
			t.Fatal("магазины не чередуются", item)
		}
		previous = item.key.tenant
		queue.started()
		queue.finished(item)
	}
	if len(queue.tenants) != 0 || len(queue.keys) != 0 {
		t.Fatal("завершённые магазины остались в очереди")
	}
	first := &delivery{key: orderingKey{kind: orderProduct, tenant: "a", entity: "p1"}}
	second := &delivery{key: first.key}
	queue.add(first)
	queue.started()
	queue.add(second)
	queue.finished(first)
	if queue.next() != second {
		t.Fatal("повторная готовность ключа не поставила магазин в круг")
	}
}

func TestRegistryBarrierWaitsForTenantAndLeavesOtherTenantsRunning(t *testing.T) {
	queue := newKeyQueue()
	first := &delivery{key: orderingKey{kind: orderProduct, tenant: "a", entity: "p1"}}
	next := &delivery{key: orderingKey{kind: orderProduct, tenant: "a", entity: "p2"}}
	registry := &delivery{key: orderingKey{kind: orderTenant, tenant: "a"}}
	other := &delivery{key: orderingKey{kind: orderProduct, tenant: "b", entity: "p1"}}
	queue.add(first)
	queue.started()
	queue.add(registry)
	queue.add(next)
	queue.add(other)
	if queue.next() != other {
		t.Fatal("ожидание реестра магазина А задержало магазин Б")
	}
	queue.started()
	if queue.next() != nil {
		t.Fatal("реестр или товар обогнал незавершённую транзакцию своего магазина")
	}
	queue.finished(first)
	if queue.next() != registry {
		t.Fatal("после завершения товара реестр не получил ход")
	}
	queue.started()
	if queue.next() != nil {
		t.Fatal("товар начался до фиксации реестра")
	}
	queue.finished(registry)
	if queue.next() != next {
		t.Fatal("товар не продолжил работу после фиксации реестра")
	}
	queue.started()
	queue.finished(next)
	queue.finished(other)
	if queue.ready.Len() != 0 || len(queue.tenants) != 0 {
		t.Fatal("барьер удерживает завершённый магазин")
	}
}
