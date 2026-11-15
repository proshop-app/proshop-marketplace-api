package catalog

import (
	"encoding/json"
	"strconv"
	"strings"
)

// IdentityNumber нормализует десятичное число без округления значащих цифр.
func IdentityNumber(value string) string {
	value = strings.TrimSpace(value)
	if !json.Valid([]byte(value)) || value == "" || (value[0] != '-' && (value[0] < '0' || value[0] > '9')) {
		return ""
	}
	mantissa, power, _ := strings.Cut(strings.ToLower(value), "e")
	exponent := int64(0)
	if power != "" {
		parsed, err := strconv.ParseInt(power, 10, 32)
		if err != nil {
			return ""
		}
		exponent = parsed
	}
	sign := ""
	if strings.HasPrefix(mantissa, "-") {
		sign = "-"
		mantissa = mantissa[1:]
	}
	if dot := strings.IndexByte(mantissa, '.'); dot >= 0 {
		exponent -= int64(len(mantissa) - dot - 1)
		mantissa = mantissa[:dot] + mantissa[dot+1:]
	}
	mantissa = strings.TrimLeft(mantissa, "0")
	if mantissa == "" {
		return "0"
	}
	trimmed := strings.TrimRight(mantissa, "0")
	exponent += int64(len(mantissa) - len(trimmed))
	mantissa = trimmed
	// Для больших порядков не разворачиваем потенциально огромную строку нулей.
	if exponent > 1024 || exponent < -1024 {
		return sign + mantissa + "e" + strconv.FormatInt(exponent, 10)
	}
	if exponent >= 0 {
		return sign + mantissa + strings.Repeat("0", int(exponent))
	}
	point := len(mantissa) + int(exponent)
	if point > 0 {
		return sign + mantissa[:point] + "." + mantissa[point:]
	}
	return sign + "0." + strings.Repeat("0", -point) + mantissa
}
