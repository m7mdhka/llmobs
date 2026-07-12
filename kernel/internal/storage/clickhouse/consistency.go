package clickhouse

import ch "github.com/ClickHouse/clickhouse-go/v2"

// ApplyReadYourWrites adds the ClickHouse connection settings that give read-after-write
// consistency ACROSS REPLICAS, to the operator-supplied options:
//
//   - insert_quorum='auto' makes a write block until a MAJORITY of replicas have acked it.
//   - select_sequential_consistency=1 makes reads return only quorum-committed rows.
//
// Together, a just-written span is immediately readable on ANY replica — closing the
// transient-404 window a distributing load balancer can otherwise open (a read landing on
// a not-yet-replicated replica).
//
// This is OFF by default and applied only when the operator opts in, because it TAXES
// writes with quorum latency. Single-node — and multi-replica behind a single/sticky
// endpoint — never need it: a replica always sees its own writes, so read-after-write
// already holds at zero cost. The operator makes the read-your-writes-vs-write-latency
// tradeoff for THEIR topology; the kernel does not impose it globally. Applied
// connection-level so every read and write inherits it uniformly.
func ApplyReadYourWrites(opts *ch.Options) {
	if opts.Settings == nil {
		opts.Settings = ch.Settings{}
	}
	opts.Settings["insert_quorum"] = "auto"
	opts.Settings["select_sequential_consistency"] = 1
}
