package synonyms

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
	"github.com/proshop-app/proshop-marketplace/internal/domain/synonyms"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

// ModelPromptVersion включается в хеш вместе с входом и провайдером.
const (
	ModelPromptVersion = "1"
	modelDailyCalls    = 4
	modelMaxConcepts   = 40
	modelMaxInput      = 15 << 10 // Оставляем место для версии, имени модели и промпта в HTTP-запросе.
)

func (s *Service) runModel(ctx context.Context, current taxonomy.Definition) error {
	if s.model == nil || current.Version == 0 {
		return nil
	}
	concepts := modelConcepts(current)
	if len(concepts) == 0 {
		return nil
	}
	id, err := modelInputKey(s.model.Identity(), concepts)
	if err != nil {
		return err
	}
	reserved, err := s.reserveModel(ctx, id)
	if err != nil || !reserved {
		return err
	}
	proposals, err := s.model.Propose(ctx, id, concepts)
	if err != nil {
		return err
	}
	nominations, err := modelNominations(id, concepts, proposals)
	if err != nil {
		return err
	}
	return s.saveModel(ctx, id, current.Version, nominations)
}

func modelConcepts(current taxonomy.Definition) []ports.SynonymModelConcept {
	concepts := make([]ports.SynonymModelConcept, 0, modelMaxConcepts)
	walkCategories(current.Categories, func(node taxonomy.Category) {
		if len(concepts) < modelMaxConcepts && len(node.Children) == 0 && node.Definition != "" {
			concepts = append(concepts, ports.SynonymModelConcept{
				ID:         node.ID,
				Name:       node.Name,
				Definition: node.Definition,
			})
		}
	})
	for len(concepts) > 0 {
		raw, err := json.Marshal(concepts)
		if err == nil && len(raw) <= modelMaxInput {
			break
		}
		concepts = concepts[:len(concepts)-1]
	}
	return concepts
}

func modelInputKey(provider string, concepts []ports.SynonymModelConcept) (string, error) {
	raw, err := json.Marshal(concepts)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte(provider+"\x00"+ModelPromptVersion+"\x00"), raw...))
	return hex.EncodeToString(sum[:]), nil
}

func (s *Service) reserveModel(ctx context.Context, id string) (bool, error) {
	reserved := false
	err := s.transact(ctx, true, func(ctx context.Context, m *session) error {
		m.rollBudget()
		if m.head.ModelInput == id || m.head.ModelCalls >= modelDailyCalls {
			return nil
		}
		reserved = true
		m.head.ModelCalls++
		return m.repo.SaveHead(ctx, m.head, nil)
	})
	return reserved, err
}

func modelNominations(
	id string,
	concepts []ports.SynonymModelConcept,
	proposals []ports.SynonymModelProposal,
) (sources, error) {
	if len(proposals) > synonyms.MaxBatch {
		return sources{}, synonyms.ErrInvalid
	}
	names := make(map[string]string, len(concepts))
	for _, concept := range concepts {
		names[concept.ID] = reference.Key(concept.Name)
	}
	data := sources{entries: make(map[string]synonyms.Nomination)}
	for _, p := range proposals {
		if !validModelProposal(p, names) {
			return sources{}, synonyms.ErrInvalid.WithMessage("некорректный ответ модели поисковых синонимов")
		}
		if p.Relation != "equivalent" {
			continue
		}
		source := synonyms.Source{Kind: synonyms.Model, ID: p.Category, Revision: id}
		n, ok := synonyms.NewNomination(p.From, p.To, p.Category, source)
		if _, seen := data.entries[n.ID]; ok && !seen {
			data.order = append(data.order, n.ID)
			data.entries[n.ID] = n
		}
	}
	return data, nil
}

func validModelProposal(p ports.SynonymModelProposal, names map[string]string) bool {
	if names[p.Category] == "" || !synonyms.ValidTerm(reference.Key(p.From)) ||
		reference.Key(p.To) != names[p.Category] {
		return false
	}
	switch p.Relation {
	case "equivalent", "broader", "narrower", "related", "uncertain":
		return true
	default:
		return false
	}
}

func (s *Service) saveModel(ctx context.Context, id string, version int64, nominations sources) error {
	return s.transact(ctx, true, func(ctx context.Context, m *session) error {
		data, err := m.sources(ctx)
		if err != nil || data.taxonomy.Version != version {
			return err
		}
		nominations, err = m.filterSources(ctx, nominations)
		if err != nil {
			return err
		}
		for _, trusted := range data.order {
			delete(nominations.entries, trusted)
		}
		if err := m.collect(ctx, nominations); err != nil {
			return err
		}
		m.head.ModelInput = id
		return m.repo.SaveHead(ctx, m.head, nil)
	})
}
