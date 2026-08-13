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

The mount experiment in `9dee9b0` exposed `/proc/device-tree/compatible` in the
running actuator DaemonSet's `volumeMounts` but produced byte-identical host
output. It was reverted in `6dd77f0`. CHILL did not answer the interactive
prompt, recorded the host output as a typed terminal Apply failure, and left
the Node untouched. A subsequent CPU-frequency sample above the MODE_50W cap
verified that the Node remained on MAXN.

Open question: does the reboot demand follow from the device hardware or from
the installed NVPModel configuration? The observations above do not distinguish
them. Resolving it requires authoritative evidence for this exact device and
software configuration, or a controlled, reboot-capable investigation outside
CHILL that audits the NVPModel configuration and repeats the transition probe.

## Decision

Treat a DeviceClass's declared power-mode set and its in-place-actuatable set as
separate facts. An actuation policy authorizes only a mode shown to be reachable
in place through the configured actuator on the tested device. Establish that
per-device fact by probing the transition and observing its result; do not infer
it from catalog presence.

For the tested `jetson-agx-orin-64g`, authorize MODE_50W and MAXN. Do not
authorize MODE_30W or MODE_15W through this actuation path on the basis of the
current observations.

Keep the existing fail-closed behavior. An interactive reboot is outside the
actuator contract, and the actuator must not answer a reboot prompt. Apply
failure remains a typed terminal failure and does not start measurement.

## Consequences

The device catalog may continue to describe all four named modes without
claiming that all four are actuation-authorized. Campaigns using this actuator
on the tested device have a two-point power-mode axis, MODE_50W and MAXN; a
four-point energy-versus-capacity curve is not obtainable through this path.

This decision does not establish a hardware restriction, describe another
Jetson or the Xavier policy, or make MODE_30W and MODE_15W generally unusable.
It records only that those two modes were not reachable in place through this
actuator on this device under the tested configuration on 2026-08-13.
