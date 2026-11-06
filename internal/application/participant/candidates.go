package participant

import (
	"context"
	"errors"
	"strings"
	"time"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	appmoderation "github.com/proshop-app/proshop-marketplace/internal/application/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func createCandidate(
	ctx context.Context,
	tx ports.Tx,
	tenant *participant.Tenant,
	identities ports.IDs,
	now time.Time,
) error {
	if tenant.Status() != participant.TenantActive {
		return nil
	}
	name := strings.TrimSpace(tenant.State().Name)
	if name == "" {
		name = tenant.Slug()
	}
	p, err := participant.New(tenant, name, now)
	if errors.Is(err, participant.ErrInvalidName) {
		p, err = participant.New(tenant, tenant.Slug(), now)
	}
	if err != nil {
		return err
	}
	if err := tx.Participants().Add(ctx, p); err != nil {
		if errors.Is(err, participant.ErrAlreadyExists) {
			return nil
		}
		return err
	}
	if err := recordCandidate(ctx, tx, p, identities, now); err != nil {
		return err
	}
	if err := tx.Outbox().Enqueue(ctx, appcatalog.SyncTenantTask(tenant.ID(), now)); err != nil {
		return err
	}
	return tx.Outbox().Enqueue(ctx, appmoderation.ReevaluateTask(tenant.ID(), now))
}

func recordCandidate(
	ctx context.Context,
	tx ports.Tx,
	p *participant.Participant,
	identities ports.IDs,
	now time.Time,
) error {
	return tx.ParticipantDecisions().Add(ctx, participant.DecisionRecord{
		ID:       identities.NewUUID(),
		TenantID: p.TenantID(),
		Actor:    "system",
		Action:   "candidate",
		Value:    string(participant.StageCandidate),
		At:       now,
	})
}

func (s *Settings) backfill(ctx context.Context, failed map[ids.TenantID]error) error {
	var after ids.TenantID
	for {
		var tenants []*participant.Tenant
		err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
			var err error
			tenants, err = tx.Registry().List(ctx, after, settingsPage)
			return err
		})
		if err != nil {
			return err
		}
		for _, tenant := range tenants {
			err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
				return createCandidate(ctx, tx, tenant, s.ids, s.clock.Now())
			})
			if err != nil {
				failed[tenant.ID()] = err
			}
			after = tenant.ID()
		}
		if len(tenants) < settingsPage {
			return nil
		}
	}
}
