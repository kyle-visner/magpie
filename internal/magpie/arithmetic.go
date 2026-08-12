package magpie

import "math"

func checkedAddCents(left, right int64, description string) (int64, error) {
	if (right > 0 && left > math.MaxInt64-right) ||
		(right < 0 && left < math.MinInt64-right) {
		return 0, appErr(ErrValidation, "%s exceeds the supported signed 64-bit cent range", description)
	}
	return left + right, nil
}

func checkedMultiplyCents(left, right int64, description string) (int64, error) {
	if left == 0 || right == 0 {
		return 0, nil
	}
	if (left == math.MinInt64 && right == -1) ||
		(right == math.MinInt64 && left == -1) {
		return 0, appErr(ErrValidation, "%s exceeds the supported signed 64-bit cent range", description)
	}
	product := left * right
	if product/right != left {
		return 0, appErr(ErrValidation, "%s exceeds the supported signed 64-bit cent range", description)
	}
	return product, nil
}

func checkedSubtractCents(left, right int64, description string) (int64, error) {
	if right == math.MinInt64 {
		return 0, appErr(ErrValidation, "%s exceeds the supported signed 64-bit cent range", description)
	}
	return checkedAddCents(left, -right, description)
}
