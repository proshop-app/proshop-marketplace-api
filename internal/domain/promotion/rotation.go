package promotion

import "sort"

// Random позволяет тестировать ротацию без глобального генератора.
type Random interface{ IntN(int) int }

// Rotate равномерно выбирает одного кандидата для каждой позиции внешним генератором.
func Rotate(candidates []CampaignState, random Random) []CampaignState {
	groups := map[int][]CampaignState{}
	positions := []int{}
	for _, candidate := range candidates {
		if candidate.Status != Active {
			continue
		}
		if len(groups[candidate.Position]) == 0 {
			positions = append(positions, candidate.Position)
		}
		groups[candidate.Position] = append(groups[candidate.Position], candidate)
	}
	sort.Ints(positions)
	result := []CampaignState{}
	for _, position := range positions {
		group := groups[position]
		result = append(result, group[random.IntN(len(group))])
	}
	return result
}
