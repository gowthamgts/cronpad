Use a local/block storage class for the ReadWriteOnce PVC; never NFS/SMB/EFS.
Apply a populated Secret, then `kubectl apply -f cronpad.yml`. The single replica
and Recreate strategy enforce one process per SQLite directory. Upgrade by
changing the image tag; allow the 45-second stop grace to drain writes.

The delivery queue is a SQLite file (`/data/cronpad-queue.db`) on the same PVC, so
nothing else needs to run. Configure TLS at an ingress and set trusted proxies in
cronpad config.

Probes use `/healthz` and `/readyz`; `kubectl exec deploy/cronpad -- cronpad
healthcheck` is the shell-free equivalent for a manual check. The startup probe
allows time for control restore; history restore is bounded at five seconds.
