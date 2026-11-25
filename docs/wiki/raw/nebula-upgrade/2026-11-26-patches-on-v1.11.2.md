# Our patches applied in order to slackhq/nebula v1.11.2 (2026-11-26)

How: branch `upgrade/nebula-1.11.2`, `go get github.com/slackhq/nebula@v1.11.2`, `go mod tidy`, `go mod vendor`, then each patch with `patch -p1 -f -N` in `vendor/`, in order, on top of the ones before. A patch that fails here may fail only because an earlier one did. Nothing was built or run.

go.mod: nebula v1.10.3 to v1.11.2, bart v0.26.0 to v0.28.0, miekg/dns v1.1.70 to v1.1.72, wireguard/windows v0.5.3 to v1.0.1, vishvananda/netlink replaced by DefinedNet/netlink v1.3.2-dn.1, go-metrics-graphite gone.

| Patch | Result |
|---|---|
| 01-graceful-shutdown | 1 out of 1 hunks failed in interface.go |
| 02-testreply-panic-fix | 1 out of 1 hunks failed in outside.go |
| 03-tun-darwin-read-buffer | 2 out of 2 hunks failed in overlay/tun_darwin.go |
| 04-batch-udp-darwin | 3 out of 3 hunks failed in udp/udp_darwin.go |
| 05-batch-udp-darwin-test | applies |
| 06-batch-tun-darwin | 3 out of 3 hunks failed in overlay/tun_darwin.go |
| 07-batch-listenin | 2 out of 3 hunks failed in interface.go |
| 08-conn-flush-interface | 1 out of 1 hunks failed in udp/udp_tester.go |
| 09-priority-queue-darwin | 7 out of 8 hunks failed in udp/udp_darwin.go |
| 10-priority-queue-tests | applies |
| 11-portmap-advertise-addr | applies |
| 12-pipeline-listenin-darwin | 1 out of 1 hunks failed in interface.go |
| 13-pipeline-listenin-test | applies |
| 14-roam-suppress-window-darwin | applies |
| 15-roam-prefer-ranges-darwin | 1 out of 2 hunks failed in outside.go |
| 16-roam-stability-tests | applies |
| 17-pipeline-listenout-darwin | 2 out of 2 hunks failed in udp/udp_darwin.go |
| 18-pipeline-listenout-test | applies |
| 19-qos-class-darwin | 3 out of 3 hunks failed in interface.go; 1 out of 1 hunks failed in udp/udp_darwin.go |
| 20-static-hostmap-inject | applies |
| 21-replace-static-hostmap | applies |
| 22-endpoint-probe | 1 out of 1 hunks failed in interface.go; 1 out of 1 hunks failed in outside.go |
| 24-flush-error-propagation | 1 out of 1 hunks failed in interface.go; 1 out of 1 hunks failed in udp/udp_darwin.go; 1 out of 1 hunks failed in handshake_manager.go |
| 25-cert-skew-tolerance | applies |
| 26-relay-manager-warn-on-drop | 1 out of 1 hunks failed in relay_manager.go |
| 27-stop-marks-closed-first | 1 out of 1 hunks failed in control.go |
| 28-darwin-send-outside-batch | 2 out of 2 hunks failed in interface.go; 11 out of 11 hunks failed in udp/udp_darwin.go |
| 29-service-close-netstack | applies |
| 30-pki-no-redundant-global-write | 1 out of 1 hunks failed in pki.go |
| 31-linux-close-releases-port-and-device | 4 out of 5 hunks failed in udp/udp_linux.go; 1 out of 1 hunks failed in overlay/tun_linux.go |
| 32-tun-close-on-exec | 1 out of 2 hunks failed in overlay/tun_linux.go |
| 33-relayed-recv-error | 1 out of 1 hunks failed in outside.go |
| 34-received-counters | 1 out of 3 hunks failed in control.go; 1 out of 2 hunks failed in outside.go |
| 35-ask-before-moving | 2 out of 2 hunks ignored in handshake_ix.go; 1 out of 3 hunks failed in hostmap.go; 5 out of 5 hunks failed in outside.go |
| 36-new-tunnel-stays | applies |
| 37-remote-addr-atomic | 1 out of 1 hunks failed in connection_manager.go; 2 out of 2 hunks failed in control.go; 2 out of 2 hunks failed in handshake_manager.go; 4 out of 4 hunks failed in hostmap.go; 3 out of 3 hunks failed in inside.go; 3 out of 4 hunks failed in outside.go; 1 out of 1 hunks failed in relay_manager.go |
| 38-relay-list-locked | 1 out of 1 hunks failed in handshake_manager.go |
