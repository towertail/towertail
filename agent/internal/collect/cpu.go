package collect

import (
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/towertail/agent/internal/schema"
)

// SampleWindow is the duration used for delta-based metrics (CPU %, net bps)
// inside a single --once invocation so the agent can stay stateless.
const SampleWindow = 200 * time.Millisecond

func CPU(window time.Duration) (schema.CPUInfo, []string) {
	var errs []string
	c := schema.CPUInfo{}

	pcts, err := cpu.Percent(window, false)
	if err != nil {
		errs = append(errs, "cpu.Percent: "+err.Error())
	} else if len(pcts) > 0 {
		c.Pct = pcts[0]
	}

	cores, err := cpu.Counts(true)
	if err != nil {
		errs = append(errs, "cpu.Counts: "+err.Error())
	} else {
		c.Cores = cores
	}

	avg, err := load.Avg()
	if err != nil {
		errs = append(errs, "load.Avg: "+err.Error())
	} else if avg != nil {
		c.Load1 = avg.Load1
		c.Load5 = avg.Load5
		c.Load15 = avg.Load15
	}

	return c, errs
}
