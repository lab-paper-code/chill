# 0017 In-Place Power-Mode Reachability Boundary

## Status

Accepted

## Context

`DeviceClass.spec.powerModes` declares the named power modes and typed actuator
targets available in a device catalog. Catalog declaration does not establish
that the configured actuator can move a particular device into every declared
mode without rebooting it.

On 2026-08-13, `jetson-agx-orin-64g` reached MAXN and MODE_50W in place through
the Jetson NVPModel actuator. Each arm produced three admitted loaded Runs and
three admitted model-loaded-idle Runs, with `epsilon` taken against the latter:

| arm | throughput items/s | P_act W | P_idle W | epsilon J/item |
|---|---:|---:|---:|---:|
| MODE_50W | 200.18 ± 0.33 | 19.151 ± 0.072 | 10.3989 ± 0.0015 | 0.04372 |
| MAXN | 265.50 ± 0.49 | 30.326 ± 0.078 | 10.4323 ± 0.0147 | 0.07493 |

MODE_30W and MODE_15W were attempted eight times in total on that device. Every
attempt ended at Apply with the same host output:

```text
apply NVPModel: exit status 234: NVPM ERROR: Failed to open /proc/device-tree/compatible! (No such file or directory)
NVPM WARN: Golden image context is already created
NVPM WARN: Reboot required for changing to this power mode: 2
NVPM WARN: DO YOU WANT TO REBOOT NOW? enter YES/yes to confirm:
NVPM ERROR: bad input!
NVPM ERROR: optMask is 1, no request for power mode
```

CHILL did not answer the interactive prompt, recorded the host output as a
typed terminal Apply failure, and left the Node untouched. A subsequent
CPU-frequency sample above the MODE_50W cap verified that the Node remained on
MAXN.

The installed configuration at `/etc/nvpmodel.conf` on the `jetsono` Node, read
through a short-lived Pod mounting that file read-only, declares:

```text
TPC_PG_MASK /sys/devices/gpu.0/tpc_pg_mask
POWER_MODEL ID=0 NAME=MAXN, TPC_POWER_GATING TPC_PG_MASK 0
POWER_MODEL ID=1 NAME=MODE_15W, TPC_POWER_GATING TPC_PG_MASK 248
POWER_MODEL ID=2 NAME=MODE_30W, TPC_POWER_GATING TPC_PG_MASK 240
POWER_MODEL ID=3 NAME=MODE_50W, TPC_POWER_GATING TPC_PG_MASK 0
```

The configuration also declares `PM_CONFIG DEFAULT=2`, the persisted
`/var/lib/nvpmodel/status` state reads `pmode:0000`, and the configuration
contains no reboot-related keyword. The reboot demand is therefore not a
per-mode reboot flag in that file.

The installed configuration assigns each mode a GPU TPC power-gating mask
written to `/sys/devices/gpu.0/tpc_pg_mask`. GPU TPC power gating on this part
is established at boot. NVPModel therefore demands a reboot exactly when the
target mode's `TPC_PG_MASK` differs from the mask currently in effect.
Reachability is a property of the transition, not of the target mode. The
evidence is not a complete pairwise test: transitions between MAXN and MODE_50W
kept mask 0 and completed in place, while attempts from MAXN at mask 0 to
MODE_30W at 240 or MODE_15W at 248 demanded a reboot.

## Decision

Treat a DeviceClass's declared power-mode set and an actuation policy's
in-place-reachable transition set as separate facts. An actuation policy
authorizes only modes whose transitions from the current and restoration modes
it can encounter stay within an in-place-reachable `TPC_PG_MASK` equivalence
class for the tested device and configuration. Establish that transition fact
from the configured masks and observed probes; do not infer it from catalog
presence or characterize reachability from the target name alone.

For the tested `jetson-agx-orin-64g` configuration, MAXN and MODE_50W form the
observed in-place class at mask 0; MODE_30W at 240 and MODE_15W at 248 are in
different classes. Keep the current policy within the mask-0 class by
authorizing MODE_50W and MAXN. Do not authorize a transition from that class to
MODE_30W or MODE_15W through this actuation path on the current evidence.

Keep the existing fail-closed behavior. An interactive reboot is outside the
actuator contract, and the actuator must not answer a reboot prompt. Apply
failure remains a typed terminal failure and does not start measurement.

## Consequences

The device catalog may continue to describe all four named modes without
claiming that every transition among them is actuation-authorized. Campaigns
using this actuator while operating and restoring within the tested mask-0
class have a two-point power-mode axis, MODE_50W and MAXN; a four-point
energy-versus-capacity curve is not obtainable through this in-place path.

**Prediction, not observation:** a MODE_15W-to-MODE_30W transition changes the
mask from 248 to 240 and is therefore predicted to demand a reboot. That
transition was not attempted.

**Prediction, not observation:** a transition between any two modes that share
the same mask is predicted not to require a reboot. MAXN and MODE_50W are the
only distinct same-mask pair in this catalog and their transitions were
observed in place; the general mask-equivalence rule was not tested on another
same-mask pair here.

This decision is limited to this device and installed configuration on
2026-08-13. It makes no claim about another Jetson device, another JetPack
version, or the Xavier row. It neither authorizes nor proposes that CHILL reboot
a Node; reboot-capable actuation remains a separate, untaken decision.
