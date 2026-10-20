#!/usr/bin/env python3
"""Compare sequential baseline/janitor runs with explicit noise tolerances."""
from pathlib import Path
import re
import sys

def read(path):
    text = Path(path).read_text()
    result = re.search(r'runs=(\d+) succeeded=(\d+) duplicates=(\d+) throughput=([\d.]+).*lag_ms p50=(\d+) p95=(\d+) p99=(\d+)', text)
    if not result:
        raise SystemExit(f'missing workload result: {path}')
    return [float(x) for x in result.groups()]

baseline, janitor = map(read, sys.argv[1:])
assert janitor[0] == baseline[0] == janitor[1] == baseline[1]
assert baseline[2] == janitor[2] == 0
assert janitor[3] >= baseline[3] * .95
# One 100ms dispatcher/flush scheduling quantum is the absolute noise budget;
# percentile ratios are unstable when the baseline is close to zero.
for name, index in [('p50', 4), ('p95', 5), ('p99', 6)]:
    assert janitor[index] <= baseline[index] + 100, (name, baseline[index], janitor[index])
assert janitor[6] < 1000
print(f'PASS janitor: same successful/unique run count, >=95% throughput, each lag percentile within baseline +100ms; baseline={baseline}, janitor={janitor}')
