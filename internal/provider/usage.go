package provider

import (
	"encoding/json"
	"math/big"
	"strings"

	"github.com/iamnalinor/qual/internal/domain"
)

func (o object) hasCounter(key string) bool {
	var count *int64
	return json.Unmarshal(o[key], &count) == nil && count != nil && *count >= 0
}

func hasCounters(o object, input, output string) bool {
	return o.hasCounter(input) && o.hasCounter(output)
}

func actualCost(data json.RawMessage) (domain.Money, bool) {
	value := strings.Trim(string(data), `"`)
	number, ok := new(big.Rat).SetString(value)
	if !ok || number.Sign() < 0 {
		return 0, false
	}
	number.Mul(number, new(big.Rat).SetInt64(1000000000))
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(number.Num(), number.Denom(), remainder)
	if remainder.Sign() > 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	if !quotient.IsInt64() {
		return 0, false
	}
	return domain.Money(quotient.Int64()), true
}
