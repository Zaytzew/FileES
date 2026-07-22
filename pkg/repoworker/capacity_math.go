package repoworker

import "math"

func capacityDecision(available, contentBytes int64) (int64, int64, error) {
	margin := int64(64 << 20)
	if contentBytes/10 > margin {
		margin = contentBytes / 10
	}
	return available, saturatingAdd(saturatingAdd(contentBytes, contentBytes), margin), nil
}

func saturatingProduct(left, right uint64) int64 {
	if right != 0 && left > uint64(math.MaxInt64)/right {
		return math.MaxInt64
	}
	return int64(left * right)
}
func saturatingAdd(left, right int64) int64 {
	if right > 0 && left > math.MaxInt64-right {
		return math.MaxInt64
	}
	return left + right
}
