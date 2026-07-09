# runtimes/shared

The shared first-party runtime host (D7). In the **lite** profile
(`runtime: shared`), the first-party plugin backends load into a single process
instead of each running as an isolated container — keeping the lite footprint
small enough to hit the D13 targets (200 spans/s on 2 vCPU / 4 GB).

The same plugins, unchanged, run as **isolated containers** in the scale
profile. The runtime host is purely a packaging/hosting choice; it grants
plugins no extra privileges — they still speak only the public plugin API
through the gateway.
