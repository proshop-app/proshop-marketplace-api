package fake

import (
	"slices"
	"sort"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// claimWalk — обход полос одного взятия; повторяет запрос взятия PostgreSQL шаг в шаг.
type claimWalk struct {
	request ports.ClaimRequest
	rows    []OutboxRow
	lanes   map[string]map[ids.TenantID][]OutboxRow
	ready   []claimLane
	last    []claimPoint
	// lastChosen — последний выбранный магазин, если пачка магазинов полна.
	lastChosen claimPoint
	chosenFull bool
}

// claimLane — готовая полоса: вид, магазин и задача, которую полоса отдаёт.
type claimLane struct {
	kind    string
	point   claimPoint
	ordered bool
	head    OutboxRow
	// oldest — полоса старейшей созревшей задачи вида: берётся раньше круга.
	oldest bool
	recent bool
}

// claimPoint — место магазина на круге от курсора: сначала после курсора, потом с начала.
type claimPoint struct {
	tenant  ids.TenantID
	wrapped bool
}

type claimCandidate struct {
	row      OutboxRow
	point    claimPoint
	position int
}

func (w *claimWalk) pending(row OutboxRow) bool {
	return row.DoneAt == nil && row.FailedAt == nil
}

func (w *claimWalk) due(row OutboxRow, kind string) bool {
	return row.Kind == kind && w.pending(row) && !row.NextAt.After(w.request.Now)
}

func (w *claimWalk) hasDue(kind string) bool {
	for _, row := range w.rows {
		if w.due(row, kind) {
			return true
		}
	}
	return false
}

func (w *claimWalk) kinds() []string {
	kinds := w.request.Kinds
	if len(kinds) == 0 {
		for _, row := range w.rows {
			if w.pending(row) && !slices.Contains(kinds, row.Kind) {
				kinds = append(kinds, row.Kind)
			}
		}
	}
	allowed := []string{}
	for _, kind := range kinds {
		if slices.Contains(w.request.ExcludeKinds, kind) || slices.Contains(allowed, kind) {
			continue
		}
		if len(w.request.Kinds) == 1 && !w.hasDue(kind) {
			continue
		}
		allowed = append(allowed, kind)
	}
	return allowed
}

// circle — магазины с незавершёнными задачами вида в порядке круга от курсора.
func (w *claimWalk) circle(kind string) []claimPoint {
	tenants := make([]ids.TenantID, 0, len(w.lanes[kind]))
	for tenant := range w.lanes[kind] {
		tenants = append(tenants, tenant)
	}
	slices.Sort(tenants)
	after := w.request.After
	points := []claimPoint{}
	for _, tenant := range tenants {
		if !after.Started || tenant > after.Tenant {
			points = append(points, claimPoint{tenant: tenant})
		}
	}
	for _, tenant := range tenants {
		if after.Started && tenant <= after.Tenant {
			points = append(points, claimPoint{tenant: tenant, wrapped: true})
		}
	}
	return points
}

// probeOldest добавляет полосу старейшей созревшей задачи вида, если она отдаёт задачу.
func (w *claimWalk) probeOldest(kind string) {
	var oldest OutboxRow
	found := false
	for _, row := range w.rows {
		if w.due(row, kind) && (!found || rowBefore(row, oldest)) {
			oldest, found = row, true
		}
	}
	if !found {
		return
	}
	ordered := slices.Contains(w.request.OrderedKinds, kind)
	head := w.head(kind, oldest.TenantID, ordered)
	if head.NextAt.After(w.request.Now) || w.slot(oldest.TenantID) <= 0 {
		return
	}
	after := w.request.After
	point := claimPoint{tenant: oldest.TenantID, wrapped: after.Started && oldest.TenantID <= after.Tenant}
	w.ready = append(w.ready, claimLane{kind: kind, point: point, ordered: ordered, head: head, oldest: true})
}

func (w *claimWalk) visit(kind string) {
	if w.lanes == nil {
		w.lanes = make(map[string]map[ids.TenantID][]OutboxRow)
	}
	lanes := make(map[ids.TenantID][]OutboxRow)
	for _, row := range w.rows {
		if row.Kind == kind && w.pending(row) {
			lanes[row.TenantID] = append(lanes[row.TenantID], row)
		}
	}
	w.lanes[kind] = lanes
	w.probeOldest(kind)
	ordered := slices.Contains(w.request.OrderedKinds, kind)
	preferHealthy := w.request.PreferRecent && !ordered && w.probeRecent(kind)
	if preferHealthy {
		w.ready = slices.DeleteFunc(w.ready, func(lane claimLane) bool {
			return lane.kind == kind && slices.Contains(w.request.DeprioritizedTenants, lane.point.tenant)
		})
	}
	visited, ready := 0, 0
	for _, point := range w.circle(kind) {
		if visited >= ports.ClaimLaneVisits || ready >= w.request.Limit {
			break
		}
		visited++
		w.markLast(point, visited)
		head := w.head(kind, point.tenant, ordered)
		if head.NextAt.After(w.request.Now) || w.slot(point.tenant) <= 0 {
			continue
		}
		if preferHealthy && slices.Contains(w.request.DeprioritizedTenants, point.tenant) {
			continue
		}
		ready++
		known := func(lane claimLane) bool { return lane.kind == kind && lane.point == point }
		if !slices.ContainsFunc(w.ready, known) {
			w.ready = append(w.ready, claimLane{kind: kind, point: point, ordered: ordered, head: head})
		}
	}
}

func (w *claimWalk) probeRecent(kind string) bool {
	var recent OutboxRow
	found := false
	for _, row := range w.rows {
		if !w.due(row, kind) || found && !rowBefore(recent, row) {
			continue
		}
		if w.slot(row.TenantID) <= 0 || slices.Contains(w.request.DeprioritizedTenants, row.TenantID) {
			continue
		}
		recent, found = row, true
	}
	if !found {
		return false
	}

	// PostgreSQL проверяет только хвост из ClaimLaneVisits строк. Проверяем позицию кандидата
	// в этом хвосте без сортировки всех созревших строк фейка.
	newer := 0
	for _, row := range w.rows {
		if w.due(row, kind) && rowBefore(recent, row) {
			newer++
			if newer == ports.ClaimLaneVisits {
				return false
			}
		}
	}

	for i := range w.ready {
		if w.ready[i].kind == kind && w.ready[i].point.tenant == recent.TenantID {
			w.ready[i].recent = true
			return true
		}
	}
	after := w.request.After
	point := claimPoint{tenant: recent.TenantID, wrapped: after.Started && recent.TenantID <= after.Tenant}
	w.ready = append(w.ready, claimLane{kind: kind, point: point, recent: true, head: recent})
	return true
}

// markLast запоминает последнюю посещённую полосу текущего вида.
func (w *claimWalk) markLast(point claimPoint, visited int) {
	if visited == 1 {
		w.last = append(w.last, point)
		return
	}
	w.last[len(w.last)-1] = point
}

// head — задача полосы: у упорядоченного вида — старейшая по id, у обычного — с ближайшим сроком.
func (w *claimWalk) head(kind string, tenant ids.TenantID, ordered bool) OutboxRow {
	var head OutboxRow
	found := false
	for _, row := range w.lanes[kind][tenant] {
		better := ordered && row.ID < head.ID ||
			!ordered && (row.NextAt.Before(head.NextAt) || row.NextAt.Equal(head.NextAt) && row.ID < head.ID)
		if !found || better {
			head, found = row, true
		}
	}
	return head
}

func (w *claimWalk) slot(tenant ids.TenantID) int {
	if slots, ok := w.request.TenantSlots[tenant]; ok {
		return slots
	}
	return w.request.PerTenant
}

// chosen — при необходимости магазины с конца очереди, затем старейшие задачи и круг.
func (w *claimWalk) chosen() ([]claimPoint, []claimPoint) {
	recent, oldest, circle := []claimPoint{}, []claimPoint{}, []claimPoint{}
	for _, lane := range w.ready {
		if lane.recent && !slices.Contains(recent, lane.point) {
			recent = append(recent, lane.point)
		}
	}
	for _, lane := range w.ready {
		if lane.oldest && !slices.Contains(recent, lane.point) && !slices.Contains(oldest, lane.point) {
			oldest = append(oldest, lane.point)
		}
	}
	sortPoints(recent)
	sortPoints(oldest)
	first := append(recent, oldest...)
	for _, lane := range w.ready {
		if !slices.Contains(first, lane.point) && !slices.Contains(circle, lane.point) {
			circle = append(circle, lane.point)
		}
	}
	sortPoints(circle)
	first = first[:min(len(first), w.request.Limit)]
	return first, circle[:min(len(circle), w.request.Limit-len(first))]
}

func (w *claimWalk) take() []ports.Task {
	first, circle := w.chosen()
	chosen := append(slices.Clone(first), circle...)
	candidates := []claimCandidate{}
	for _, point := range chosen {
		quota := min(w.slot(point.tenant), w.request.Limit-len(chosen)+1)
		candidates = append(candidates, w.tenantTasks(point, quota)...)
	}
	sort.SliceStable(candidates, func(a, b int) bool {
		left, right := candidates[a], candidates[b]
		if left.position != right.position {
			return left.position < right.position
		}
		if left.point != right.point {
			return pointBefore(left.point, right.point)
		}
		return rowBefore(left.row, right.row)
	})
	tasks := []ports.Task{}
	for _, candidate := range candidates[:min(len(candidates), w.request.Limit)] {
		row := candidate.row
		tasks = append(tasks, ports.Task{
			ID:              row.ID,
			Kind:            row.Kind,
			TenantID:        row.TenantID,
			Payload:         row.Payload,
			UnknownFailures: row.UnknownFailures,
			CreatedAt:       row.CreatedAt,
		})
	}
	w.chosenFull = len(chosen) == w.request.Limit && len(circle) > 0
	if w.chosenFull {
		w.lastChosen = circle[len(circle)-1]
	}
	return tasks
}

func (w *claimWalk) tenantTasks(point claimPoint, quota int) []claimCandidate {
	rows := []OutboxRow{}
	for _, lane := range w.ready {
		if lane.point != point {
			continue
		}
		if lane.ordered {
			rows = append(rows, lane.head)
			continue
		}
		due := []OutboxRow{}
		for _, row := range w.lanes[lane.kind][point.tenant] {
			if !row.NextAt.After(w.request.Now) {
				due = append(due, row)
			}
		}
		sort.Slice(due, func(a, b int) bool { return rowBefore(due[a], due[b]) })
		rows = append(rows, due[:min(len(due), quota)]...)
	}
	sort.Slice(rows, func(a, b int) bool { return rowBefore(rows[a], rows[b]) })
	candidates := []claimCandidate{}
	for n, row := range rows[:min(len(rows), quota)] {
		candidates = append(candidates, claimCandidate{row: row, point: point, position: n + 1})
	}
	return candidates
}

// next — курсор: самая ранняя по кругу из последних посещённых полос видов и, при полной
// пачке, последний выбранный магазин.
func (w *claimWalk) next() ports.ClaimCursor {
	edges := slices.Clone(w.last)
	if w.chosenFull {
		edges = append(edges, w.lastChosen)
	}
	if len(edges) == 0 {
		return w.request.After
	}
	sortPoints(edges)
	return ports.ClaimAfter(edges[0].tenant)
}

func sortPoints(points []claimPoint) {
	sort.Slice(points, func(a, b int) bool { return pointBefore(points[a], points[b]) })
}

func pointBefore(left, right claimPoint) bool {
	if left.wrapped != right.wrapped {
		return !left.wrapped
	}
	return left.tenant < right.tenant
}

func rowBefore(left, right OutboxRow) bool {
	if !left.NextAt.Equal(right.NextAt) {
		return left.NextAt.Before(right.NextAt)
	}
	return left.ID < right.ID
}

// TasksOf — задачи взятия для проверок тестов, где курсор круга не важен.
func TasksOf(result ports.ClaimResult, err error) ([]ports.Task, error) {
	return result.Tasks, err
}
