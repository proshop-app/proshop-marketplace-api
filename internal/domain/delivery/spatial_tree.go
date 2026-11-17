package delivery

import (
	"slices"
)

// В плотной граничной ячейке дерево не даёт перейти к линейному обходу всего города.
const pickupLeafSize = 32

type pickupNode struct {
	bounds Bounds
	count  int
	points []PickupPoint
	left   *pickupNode
	right  *pickupNode
}

func newPickupNode(points []PickupPoint, depth int) *pickupNode {
	node := &pickupNode{count: len(points)}
	if len(points) <= pickupLeafSize {
		node.points = points
		for index, point := range points {
			b := Bounds{point.Location.Lat, point.Location.Lon, point.Location.Lat, point.Location.Lon}
			if index == 0 {
				node.bounds = b
			} else {
				node.bounds = unionPickupBounds(node.bounds, b)
			}
		}
		return node
	}
	slices.SortFunc(points, func(a, b PickupPoint) int {
		first, second := a.Location.Lat, b.Location.Lat
		if depth%2 == 1 {
			first, second = a.Location.Lon, b.Location.Lon
		}
		if first < second {
			return -1
		}
		if first > second {
			return 1
		}
		return 0
	})
	middle := len(points) / 2
	node.left = newPickupNode(points[:middle], depth+1)
	node.right = newPickupNode(points[middle:], depth+1)
	node.bounds = unionPickupBounds(node.left.bounds, node.right.bounds)
	return node
}

func (n *pickupNode) sample(result *PickupArea) {
	if len(result.Items) >= PickupMapLimit {
		return
	}
	if n.left == nil {
		count := min(len(n.points), PickupMapLimit-len(result.Items))
		result.Items = append(result.Items, n.points[:count]...)
		return
	}
	n.left.sample(result)
	n.right.sample(result)
}

func (n *pickupNode) query(box Bounds, result *PickupArea) {
	b := n.bounds
	if box[0] > b[2] || box[2] < b[0] || box[1] > b[3] || box[3] < b[1] {
		return
	}
	if box[0] <= b[0] && box[1] <= b[1] && box[2] >= b[2] && box[3] >= b[3] {
		result.Total += n.count
		n.sample(result)
		return
	}
	if n.left != nil {
		n.left.query(box, result)
		n.right.query(box, result)
		return
	}
	for _, point := range n.points {
		if box.contains(point.Location) {
			result.Total++
			if len(result.Items) < PickupMapLimit {
				result.Items = append(result.Items, point)
			}
		}
	}
}

func unionPickupBounds(first, second Bounds) Bounds {
	return Bounds{
		min(first[0], second[0]),
		min(first[1], second[1]),
		max(first[2], second[2]),
		max(first[3], second[3]),
	}
}
