package config

import "time"

func (r *reader) optionalInterval(name string, fallback time.Duration) time.Duration {
	raw, ok := r.lookup(name)
	if !ok {
		return fallback
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value < 0 {
		r.fail(name, "ожидается неотрицательная длительность")
		return fallback
	}
	return value
}
