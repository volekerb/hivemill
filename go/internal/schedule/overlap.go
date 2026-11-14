package schedule

import (
	"fmt"
	"strconv"
	"strings"
)

// HourOverlapWarning returns a stderr line when hour matches a fixed schedule, or "".
func HourOverlapWarning(hour int, digestHour, retroHour, scoutSlots string) string {
	if h, err := strconv.Atoi(strings.TrimSpace(digestHour)); err == nil && h == hour {
		return fmt.Sprintf("warning: hour %d matches digest_hour=%s", hour, strings.TrimSpace(digestHour))
	}
	if h, err := strconv.Atoi(strings.TrimSpace(retroHour)); err == nil && h == hour {
		return fmt.Sprintf("warning: hour %d matches retro_hour=%s", hour, strings.TrimSpace(retroHour))
	}
	for _, p := range strings.Split(scoutSlots, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		h, err := strconv.Atoi(p)
		if err == nil && h == hour {
			return fmt.Sprintf("warning: hour %d matches scout_slots=%s", hour, strings.TrimSpace(scoutSlots))
		}
	}
	return ""
}
