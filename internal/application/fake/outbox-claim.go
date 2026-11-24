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
	index   map[int64]int
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
		if !slices.Contains(w.request.ExcludeKinds, kind) && !slices.Contains(allowed, kind) {
			allowed = append(allowed, kind)
		}
	}
	return allowed
}

// circle — магазины с незавершёнными задачами вида в порядке круга от курсора.
func (w *claimWalk) circle(kind string) []claimPoint {
	tenants := []ids.TenantID{}
	for _, row := range w.rows {
		if w.pending(row) && row.Kind == kind && !slices.Contains(tenants, row.TenantID) {
			tenants = append(tenants, row.TenantID)
		}
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
		due := w.pending(row) && row.Kind == kind && !row.NextAt.After(w.request.Now)
		if due && (!found || rowBefore(row, oldest)) {
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
	w.probeOldest(kind)
	ordered := slices.Contains(w.request.OrderedKinds, kind)
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
		ready++
		known := func(lane claimLane) bool { return lane.kind == kind && lane.point == point }
		if !slices.ContainsFunc(w.ready, known) {
			w.ready = append(w.ready, claimLane{kind: kind, point: point, ordered: ordered, head: head})
		}
	}
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
	for _, row := range w.rows {
		if !w.pending(row) || row.Kind != kind || row.TenantID != tenant {
			continue
		}
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

// chosen — магазины старейших задач видов, затем по кругу; всего не больше Limit.
func (w *claimWalk) chosen() ([]claimPoint, []claimPoint) {
	first, circle := []claimPoint{}, []claimPoint{}
	for _, lane := range w.ready {
		if lane.oldest && !slices.Contains(first, lane.point) {
			first = append(first, lane.point)
		}
	}
	for _, lane := range w.ready {
		if !lane.oldest && !slices.Contains(first, lane.point) && !slices.Contains(circle, lane.point) {
			circle = append(circle, lane.point)
		}
	}
	sortPoints(first)
	sortPoints(circle)
	first = first[:min(len(first), w.request.Limit)]
	return first, circle[:min(len(circle), w.request.Limit-len(first))]
}

func (w *claimWalk) take() []ports.Task {
	w.index = make(map[int64]int, len(w.rows))
	for n, row := range w.rows {
		w.index[row.ID] = n
	}
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
		for _, row := range w.rows {
			if w.pending(row) && row.Kind == lane.kind && row.TenantID == point.tenant &&
				!row.NextAt.After(w.request.Now) {
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
