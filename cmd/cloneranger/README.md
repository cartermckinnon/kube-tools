cloneranger
=============

cloneranger helps simulate large Kubernetes clusters by creating "clone" Node objects and associated NodeLeases. A DaemonSet runs `kwok` on a set of template nodes; each kwok instance manages clones that reference the template node.

Files
- `main.go` - CLI that supports `up` and `down` subcommands to create/delete clones.
- `deploy/cloneranger/` - kustomize manifests (Namespace, ServiceAccount, ClusterRole, ClusterRoleBinding, DaemonSet).

Deploy with kustomize

The deploy manifests are Kustomize-friendly. To apply with the default image:

```sh
kubectl apply -k deploy/cloneranger
```

To override the kwok image (example):

```sh
kustomize edit set image ghcr.io/kubewharf/kwok=myrepo/kwok:mytag
kubectl apply -k deploy/cloneranger
```

Usage examples

Build the CLI:

```sh
go build -o bin/cloneranger ./cmd/cloneranger
```

Create 50 clones per template node (dry run):

```sh
./bin/cloneranger up --per-template 50 --dry-run
```

Delete all clones:

```sh
./bin/cloneranger down
```

Notes
Notes
- The DaemonSet is configured to run in the `cloneranger-system` namespace and uses the `cloneranger-kwok` ServiceAccount. RBAC is provided by the included ClusterRole and ClusterRoleBinding.
- Leases are created in the `kube-node-lease` namespace. Ensure the namespace exists (Kubernetes control plane typically creates it).
