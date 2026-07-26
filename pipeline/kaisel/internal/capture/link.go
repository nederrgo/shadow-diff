package capture

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/shadow-diff/kaisel/internal/decode"
)

// DetectFraming reads an interface's ARPHRD type from sysfs and maps it to the
// link-layer framing.
//
// ponytail: sysfs rather than netlink -- one file read, no extra dependency.
// Ceiling: it cannot describe interfaces that report ARPHRD_ETHER while
// carrying bare L3 frames (some tunnel setups). decode.Packet's fallback
// covers that case, and -l2-off forces the value outright.
func DetectFraming(iface string) (decode.Framing, error) {
	b, err := os.ReadFile("/sys/class/net/" + iface + "/type")
	if err != nil {
		return 0, fmt.Errorf("read link type for %q: %w", iface, err)
	}
	t, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 32)
	if err != nil {
		return 0, fmt.Errorf("parse link type for %q: %w", iface, err)
	}
	return decode.FramingFromARPHRD(uint32(t)), nil
}
