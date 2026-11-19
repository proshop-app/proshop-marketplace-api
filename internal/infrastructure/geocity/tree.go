package geocity

import "sort"

type node struct {
	item  entry
	axis  int
	left  *node
	right *node
}

func build(items []entry, depth int) *node {
	if len(items) == 0 {
		return nil
	}
	axis := depth % 3
	sort.Slice(items, func(i, j int) bool { return items[i].point[axis] < items[j].point[axis] })
	mid := len(items) / 2
	return &node{
		item:  items[mid],
		axis:  axis,
		left:  build(items[:mid], depth+1),
		right: build(items[mid+1:], depth+1),
	}
}

func (n *node) nearest(point [3]float64, best entry, distance float64) (entry, float64) {
	if n == nil {
		return best, distance
	}
	d := squaredDistance(point, n.item.point)
	if d < distance {
		best, distance = n.item, d
	}
	delta := point[n.axis] - n.item.point[n.axis]
	first, second := n.left, n.right
	if delta > 0 {
		first, second = second, first
	}
	best, distance = first.nearest(point, best, distance)
	if delta*delta <= distance {
		best, distance = second.nearest(point, best, distance)
	}
	return best, distance
}

func squaredDistance(a, b [3]float64) float64 {
	dx, dy, dz := a[0]-b[0], a[1]-b[1], a[2]-b[2]
	return dx*dx + dy*dy + dz*dz
}
