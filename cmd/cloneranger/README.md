# `cloneranger`

---

`cloneranger` helps simulate Kubernetes clusters with many nodes.

A DaemonSet runs `kwok` on a set of template nodes; each `kwok` instance manages that node's clones.
The `cloneranger` CLI creates and destroys the clones.

Most of the heavy-lifting is done by `kwok`, but the simulation is made more accurate by "cloning" a set of real nodes.
This does a few things:

1. Cloned nodes use real identifiers such as `providerID`, hardware labels, etc. This minimizes no-op handling in the control plane and other controllers.
2. The size of the Node object is accurate. This puts the same pressure on `kube-apiserver`/`etcd` as real nodes.
3. Network traffic to the control plane is more accurate vs. having a single `kwok` instance.

## Usage

1. Deploy the `kwok`-s:
```sh
kubectl apply -k deploy/cloneranger
```

2. Create clones:
```sh
cloneranger up -n 10
```

3. Delete clones:
```sh
cloneranger down
```
