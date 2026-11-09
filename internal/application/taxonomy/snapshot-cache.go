package taxonomy

import (
	"context"
	"maps"
	"sync"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

const snapshotCacheLimit = 16

type snapshotCache struct {
	mu      sync.Mutex
	entries []policySnapshot
}

// policySnapshot содержит общие с кэшем значения: карты доступны только для чтения.
type policySnapshot struct {
	Version  int64
	Tree     map[string]categoryNode
	Policies map[string]string
}

type categoryNode struct {
	Name   string
	Parent string
}

func (s *Service) snapshot(ctx context.Context, tx ports.Tx, version int64) (policySnapshot, error) {
	// Кэш живёт вместе с сервисом процесса, но не смешивает версии разных хранилищ.
	// Голова не кэшируется: её чтение удерживает блокировку публикации до конца пачки.
	c := &s.snapshots
	c.mu.Lock()
	if d, ok := c.find(version); ok {
		c.mu.Unlock()
		return d, nil
	}
	c.mu.Unlock()
	d, err := tx.Taxonomy().Version(ctx, version)
	if err != nil {
		return policySnapshot{}, err
	}
	snapshot := policySnapshot{Version: version, Tree: categoryTree(d.Categories), Policies: categoryPolicies(d)}
	c.mu.Lock()
	defer c.mu.Unlock()
	if d, ok := c.find(version); ok {
		return d, nil
	}
	c.entries = append([]policySnapshot{snapshot}, c.entries...)
	if len(c.entries) > snapshotCacheLimit {
		c.entries = c.entries[:snapshotCacheLimit]
	}
	return snapshot, nil
}

// find вызывается под мьютексом кэша.
func (c *snapshotCache) find(version int64) (policySnapshot, bool) {
	for i, d := range c.entries {
		if d.Version == version {
			copy(c.entries[1:i+1], c.entries[:i])
			c.entries[0] = d
			return d, true
		}
	}
	return policySnapshot{}, false
}

func categoryTree(categories []taxonomy.Category) map[string]categoryNode {
	nodes := map[string]categoryNode{}
	var visit func([]taxonomy.Category, string)
	visit = func(items []taxonomy.Category, parent string) {
		for _, c := range items {
			nodes[c.ID] = categoryNode{Name: c.Name, Parent: parent}
			visit(c.Children, c.ID)
		}
	}
	visit(categories, "")
	return nodes
}

func (s policySnapshot) policy(id string) string {
	if policy, ok := s.Policies[id]; ok {
		return policy
	}
	return taxonomy.DefaultModerationPolicy
}

// Любое отличие дерева требует освежить сводки всех магазинов прохода.
func treeNeedsRefresh(old, next map[string]categoryNode) bool {
	return !maps.Equal(old, next)
}

func categoryPolicies(d taxonomy.Definition) map[string]string {
	policies := map[string]string{}
	var visit func([]taxonomy.Category)
	visit = func(nodes []taxonomy.Category) {
		for _, node := range nodes {
			policies[node.ID] = d.ModerationPolicy(node.ID)
			visit(node.Children)
		}
	}
	visit(d.Categories)
	return policies
}
