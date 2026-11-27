package factorytest

import (
	"math"
	"os"
	"strconv"
)

// TimeScale is $HM_TEST_TIME_SCALE, which stretches test time limits on a busy CI host; unset or invalid means 1.
func TimeScale() float64 {
	v, err := strconv.ParseFloat(os.Getenv("HM_TEST_TIME_SCALE"), 64)
	if err != nil || !(v > 0) || math.IsInf(v, 1) {
		return 1
	}
	return v
}
