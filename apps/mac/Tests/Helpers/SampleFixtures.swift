import Foundation
@testable import Towertail

func makeSample(
    cpuPct: Double,
    memFrac: Double,
    diskFrac: Double,
    rxBps: Int64 = 0,
    txBps: Int64 = 0,
    t: Date = Date()
) -> Sample {
    let totalMem: Int64 = 16_000_000_000
    let totalDisk: Int64 = 100_000_000_000
    return Sample(
        v: 1, ts: t,
        host: HostInfo(name: "h", os: "linux", arch: "arm64", kernel: "6.6", uptimeS: 1, sampler: "x", machineID: nil),
        cpu: CPUInfo(pct: cpuPct, load1: 0, load5: 0, load15: 0, cores: 4),
        mem: MemInfo(used: Int64(Double(totalMem) * memFrac), total: totalMem),
        swap: MemInfo(used: 0, total: 0),
        disks: [DiskSample(mount: "/", fs: "apfs", used: Int64(Double(totalDisk) * diskFrac), total: totalDisk)],
        net: NetInfo(rxBps: rxBps, txBps: txBps, rxCum: 0, txCum: 0),
        errors: []
    )
}
