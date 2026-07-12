package clickhouse

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

// minServerMajor/minServerMinor is the lowest ClickHouse version at which the erasure
// guarantee this adapter depends on is reliable: lightweight DELETE is GA and
// `apply_deleted_mask` (the read-time setting that EXCLUDES lightweight-deleted rows
// from every SELECT) is honored. Below this, a GDPR-erased span could be read back —
// so the adapter refuses to start rather than silently serve erased data (#88). 23.8 is
// the first LTS where lightweight deletes + the deleted-mask read semantics are stable.
const (
	minServerMajor = 23
	minServerMinor = 8
)

// versionProbe is the narrow read surface probeServer needs (satisfied by driver.Conn).
type versionProbe interface {
	QueryRow(ctx context.Context, query string, args ...any) driver.Row
}

// ServerInfo is the result of probing a live ClickHouse before serving reads.
type ServerInfo struct {
	Version                string // raw version() string, e.g. "24.3.1.2823"
	HasLazyMaterialization bool   // whether query_plan_optimize_lazy_materialization exists
}

// ProbeServer reads version(), enforces the erasure version floor (FAIL LOUD, #88), and
// feature-detects the lazy-materialization setting. A self-hoster on a ClickHouse too
// old to honor the deleted mask must not boot, because reads there could resurrect
// GDPR-erased spans. Feature-detecting the lazy-materialization setting (via
// system.settings) — rather than hardcoding a version threshold — lets us disable that
// optimization on the versions that HAVE it (where a plan reorder could read a
// lightweight-deleted row past the mask) while never sending an UNKNOWN_SETTING to a
// version that does not.
func ProbeServer(ctx context.Context, c versionProbe) (ServerInfo, error) {
	var info ServerInfo
	if err := c.QueryRow(ctx, "SELECT version()").Scan(&info.Version); err != nil {
		return info, fmt.Errorf("reading ClickHouse server version: %w", err)
	}
	if err := checkVersionFloor(info.Version); err != nil {
		return info, err
	}
	var n uint64
	if err := c.QueryRow(ctx,
		"SELECT count() FROM system.settings WHERE name = 'query_plan_optimize_lazy_materialization'").
		Scan(&n); err != nil {
		return info, fmt.Errorf("probing lazy-materialization setting: %w", err)
	}
	info.HasLazyMaterialization = n > 0
	return info, nil
}

// checkVersionFloor is the pure fail-loud guard (#88): a ClickHouse below the erasure
// floor is refused with an actionable message. Split from the I/O so the load-bearing
// comparison is hermetically tested.
func checkVersionFloor(version string) error {
	maj, min, err := parseVersion(version)
	if err != nil {
		return err
	}
	if maj < minServerMajor || (maj == minServerMajor && min < minServerMinor) {
		return fmt.Errorf(
			"ClickHouse %s is below the minimum %d.%d required for reliable GDPR erasure: "+
				"lightweight DELETE + apply_deleted_mask must be honored so an erased span is never "+
				"read back (#88). Upgrade ClickHouse to at least %d.%d",
			version, minServerMajor, minServerMinor, minServerMajor, minServerMinor)
	}
	return nil
}

// parseVersion extracts major.minor from a ClickHouse version() string
// ("24.3.1.2823" → 24, 3). It tolerates any suffix after the first two components.
func parseVersion(v string) (int, int, error) {
	parts := strings.SplitN(strings.TrimSpace(v), ".", 3)
	if len(parts) < 2 {
		return 0, 0, fmt.Errorf("unrecognized ClickHouse version string %q", v)
	}
	maj, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, fmt.Errorf("unrecognized ClickHouse major version in %q: %w", v, err)
	}
	min, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, fmt.Errorf("unrecognized ClickHouse minor version in %q: %w", v, err)
	}
	return maj, min, nil
}
