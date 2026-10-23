package money

import "strconv"

// MarshalJSON сохраняет деньги целыми копейками без промежуточного float.
func (m Money) MarshalJSON() ([]byte, error) { return []byte(strconv.FormatInt(m.kopecks, 10)), nil }

// UnmarshalJSON принимает только целые копейки в диапазоне int64.
func (m *Money) UnmarshalJSON(raw []byte) error {
	value, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil {
		return ErrInvalid.Wrap(err)
	}
	m.kopecks = value
	return nil
}
