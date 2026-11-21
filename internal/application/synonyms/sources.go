package synonyms

import (
	"context"
	"strconv"

	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
	"github.com/proshop-app/proshop-marketplace/internal/domain/synonyms"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

const collectLimit = 20

type sources struct {
	taxonomy taxonomy.Definition
	entries  map[string]synonyms.Nomination
	order    []string
	overflow bool
}

type sourceKey struct {
	category string
	from     string
}

func (m *session) sources(ctx context.Context) (sources, error) {
	current, err := m.tx.Taxonomy().Current(ctx)
	if err != nil {
		return sources{}, err
	}
	inputs, err := m.repo.Inputs(ctx)
	if err != nil {
		return sources{}, err
	}
	data := sources{
		taxonomy: current,
		entries:  make(map[string]synonyms.Nomination),
		overflow: len(inputs) > synonyms.MaxCandidates,
	}
	categories := categoryIndex(current.Categories)
	revision := strconv.FormatInt(current.Version, 10)
	for _, input := range inputs {
		category, exists := categories[input.Category]
		if current.Version == 0 || !exists || reference.Key(category.Name) != reference.Key(input.CategoryName) ||
			(input.Source.Kind == synonyms.RegistryAlias && len(category.Children) != 0) {
			continue
		}
		source := input.Source
		source.Revision = revision
		if input.Source.Revision != "" {
			source.Revision += ":" + input.Source.Revision
		}
		nomination, ok := synonyms.NewNomination(input.From, input.To, input.Category, source)
		if _, exists := data.entries[nomination.ID]; ok && !exists {
			data.entries[nomination.ID] = nomination
			data.order = append(data.order, nomination.ID)
		}
	}
	if data.overflow {
		data.order = nil
		return data, nil
	}
	return m.filterSources(ctx, data)
}

func (m *session) filterSources(ctx context.Context, data sources) (sources, error) {
	keys, err := m.blockedKeys(ctx, data)
	if err != nil {
		return sources{}, err
	}
	seen := map[sourceKey]string{}
	conflicts := map[sourceKey]bool{}
	for id, n := range data.entries {
		key := sourceKey{category: n.Scope.ID, from: n.From}
		if previous, ok := seen[key]; ok && previous != n.To {
			conflicts[key] = true
		}
		seen[key] = n.To
		if n.Blocked(keys) {
			delete(data.entries, id)
		}
	}
	kept := data.order[:0]
	for _, id := range data.order {
		n, ok := data.entries[id]
		if ok && !conflicts[sourceKey{category: n.Scope.ID, from: n.From}] {
			kept = append(kept, id)
		} else {
			delete(data.entries, id)
		}
	}
	data.order = kept
	return data, nil
}

func (m *session) blockedKeys(ctx context.Context, data sources) (map[string]bool, error) {
	parts := make(map[string]bool)
	for _, n := range data.entries {
		for _, term := range []string{n.From, n.To} {
			parts[term] = true
			for _, part := range synonyms.BoundaryTerms(term) {
				parts[part] = true
			}
		}
	}
	keys := make([]string, 0, len(parts))
	for key := range parts {
		keys = append(keys, key)
	}
	brands, err := m.repo.Brands(ctx, keys)
	if err != nil {
		return nil, err
	}
	return keySet(append(brands, m.head.BlockedTerms...)), nil
}

func categoryIndex(nodes []taxonomy.Category) map[string]taxonomy.Category {
	result := map[string]taxonomy.Category{}
	walkCategories(nodes, func(node taxonomy.Category) {
		result[node.ID] = node
	})
	return result
}

func walkCategories(nodes []taxonomy.Category, visit func(taxonomy.Category)) {
	for _, node := range nodes {
		visit(node)
		walkCategories(node.Children, visit)
	}
}

func (m *session) collect(ctx context.Context, data sources) error {
	known, err := m.repo.Known(ctx, data.order)
	if err != nil {
		return err
	}
	live, err := m.repo.LiveCount(ctx, m.now)
	if err != nil {
		return err
	}
	byID := make(map[string]synonyms.Entry, len(known))
	for _, entry := range known {
		byID[entry.ID] = entry
	}
	writes := 0
	for _, id := range data.order {
		if writes == collectLimit || live >= synonyms.MaxCandidates {
			break
		}
		n, retained := data.entries[id]
		if !retained {
			continue
		}
		entry, exists := byID[id]
		changed, grows, err := m.collectOne(ctx, n, entry, exists)
		if err != nil {
			return err
		}
		if changed {
			writes++
		}
		if grows {
			live++
		}
	}
	return nil
}

func (m *session) collectOne(
	ctx context.Context,
	n synonyms.Nomination,
	entry synonyms.Entry,
	exists bool,
) (bool, bool, error) {
	if !exists {
		return true, true, m.add(ctx, n)
	}
	if entry.Phase == synonyms.Disabled || entry.Source == n.Source ||
		(n.Source.Kind == synonyms.Model && entry.Source.Kind != synonyms.Model) {
		return false, false, nil
	}
	prior := entry
	entry.Refresh(n.Source, m.now)
	grows := entry.Phase == synonyms.Quarantined && !prior.ExpiresAt.After(m.now)
	return true, grows, m.repo.Save(ctx, prior, entry)
}

func (m *session) add(ctx context.Context, n synonyms.Nomination) error {
	entry := synonyms.NewEntry(n, m.now)
	if err := m.repo.Insert(ctx, entry); err != nil {
		return err
	}
	event := synonyms.Transition{To: synonyms.Candidate, At: m.now, Actor: "worker", Reason: "source_collected"}
	if err := m.repo.RecordTransition(ctx, entry.ID, event); err != nil {
		return err
	}
	before := entry
	move, err := entry.Quarantine(m.now)
	if err != nil {
		return err
	}
	return m.transition(ctx, before, entry, move)
}

func (m *session) prune(ctx context.Context, data sources) ([]synonyms.Entry, bool, error) {
	active, err := m.repo.Active(ctx)
	if err != nil {
		return nil, false, err
	}
	keep := active[:0]
	for _, entry := range active {
		if _, valid := data.entries[entry.ID]; valid || data.overflow {
			keep = append(keep, entry)
			continue
		}
		if err := m.disable(ctx, entry, "worker", "source_missing_or_blocked"); err != nil {
			return nil, false, err
		}
	}
	return keep, len(keep) != len(active), nil
}
