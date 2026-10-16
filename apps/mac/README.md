# apps/mac

Swift / SwiftUI macOS menu-bar application. Released on GitHub Releases. Refer to [Phase 1 in PLAN.md](../../docs/PLAN.md#roadmap-phases).

Xcode project lives here; see [`../../docs/PLAN.md`](../../docs/PLAN.md) §3 for the module layout and [`wire-format.md`](https://github.com/towertail/sampler/blob/main/docs/wire-format.md) for how the app boots/invokes the remote collector.

## Full Disk Access in dev builds

The Usage sub-tab of DISK scans the home folder. Without Full Disk Access, the scan cannot read Mail, Messages and Safari data. An ad-hoc signed build gets a new code signature on each build, so macOS forgets the grant. Sign with your Developer ID (`SIGN_IDENTITY`) to keep the grant, or give the grant again after each build.
