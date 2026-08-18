# Kubernetes provider for Cloud API Adaptor

This project is a complete Kubernetes backend for Cloud API Adaptor (CAA). It
turns an ordinary infrastructure Kubernetes cluster into a provider for
peer-pod sandboxes and runs each requested sandbox as an isolated PodVM. It is
provider-neutral: it does not depend on mNi Cloud, Juneau, or a particular
hosted-control-plane implementation.

The project exists so that a platform operator does not have to implement a
cloud-specific CAA provider, sandbox lifecycle controller, and VMM launcher in
every environment. CAA receives the standard workload Pod request; this
project translates the sandbox operation into Kubernetes resources on the
infrastructure cluster and manages the corresponding PodVM lifecycle.

The value is broader than PeerPodNode:

- **Use Kubernetes as the infrastructure API.** Placement, host capabilities,
  image distribution, reconciliation, and failure recovery can use normal
  Kubernetes mechanisms instead of a proprietary cloud API.
- **Keep the workload Kubernetes contract.** Pods remain Pods in the workload
  cluster. The integration preserves the kubelet, CRI, logs, exec, probes,
  Secrets, Services, and scheduler path supplied by CAA and Kata rather than
  translating a Pod into a different customer-facing resource.
- **Separate policy from implementation.** `PodSandboxClass` selects placement,
  networking metadata, the PodVM artifact, and the runner implementation. The
  controller is independent of a particular VMM.
- **Reuse one backend in different products.** The same provider supports an
  existing Kubernetes cluster, a hybrid KaaS, or a serverless-only KaaS. A
  hosted control plane such as Kamaji is an integration choice, not a project
  dependency.

It provides five cooperating artifacts:

- a CAA Kubernetes cloud-provider plugin that maps CAA operations to the
  provider API;
- an authenticated Pod Sandbox API and the `PodSandbox` / `PodSandboxClass`
  resource model;
- a controller that reconciles `PodSandbox` resources into runner Pods and
  reports sandbox addresses and readiness;
- runner and PodVM artifact images for a concrete VMM implementation;
- an optional `PeerPodNode` image for KaaS providers that need a real
  kubelet/containerd/CAA path without provisioning a conventional worker VM.

The KaaS implementation remains responsible for control-plane lifecycle,
cluster credentials, CNI configuration, PeerPodNode lifecycle, upgrades,
draining, and scaling.

## Who uses it

The intended user is a Kubernetes platform or managed-service operator. An
application developer continues to submit ordinary Pods, Deployments, Jobs,
and Services to the workload cluster; they do not call the Pod Sandbox API or
manage `PodSandbox` resources directly.

The operator installs the provider backend on an infrastructure Kubernetes
cluster and connects CAA from one or more workload clusters. The operator may
use the released PeerPodNode image, or install CAA on ordinary workload
workers. The surrounding service remains responsible for control planes,
tenant identity, networking, credentials, quotas, upgrades, and physical host
capacity.

## What can be built with it

- **Peer-pod infrastructure for an existing cluster:** keep normal worker
  Nodes, but send selected RuntimeClass workloads to PodVMs on a separate
  infrastructure Kubernetes cluster.
- **Hybrid managed Kubernetes:** combine customer-visible worker pools with a
  provider-managed peer-pod execution path in one workload cluster.
- **Serverless-only managed Kubernetes:** pair a hosted control plane with
  provider-managed PeerPodNodes so customers deploy Pods without creating a
  worker pool.
- **Kubernetes-on-Kubernetes KaaS:** use Kamaji or another control-plane
  implementation and use this project as the reusable worker/sandbox backend.
- **VMM-specific offerings behind one API:** offer Cloud Hypervisor initially
  and introduce QEMU, Firecracker, or Dragonball runner images without changing
  the PodSandbox controller contract.

This project does not create the hosted control plane, expose a tenant-facing
Cluster API, decide billing plans, or operate physical-node autoscaling. Those
are responsibilities of the service built on top of it. Its boundary is the
reusable peer-pod execution backend and the artifacts required to connect a
real Kubernetes node path to that backend.

## Runner contract

`PodSandboxClass.spec.runnerImage` selects the VMM implementation. The
controller only relies on this image contract:

- the executable is `/runner`;
- `run` accepts CPU, memory, MTU, image/config/state directories, and a mounted
  runtime-assets directory;
- `ready` returns success only after the guest and its CAA agent are ready;
- `stop` shuts the guest down;
- the PodVM artifact image writes implementation-specific files to `/output`.

The currently released implementation is
`runner-cloud-hypervisor`. A QEMU, Firecracker, or Dragonball implementation
can be added as another runner image without changing the controller or
PeerPodNode. All implementations must preserve the command and readiness
semantics above.

`runtimeAssetsDir` names the host directory mounted read-only at
`/opt/podvm-runtime`. It replaces the former Cloud-Hypervisor-specific
`kataDir` API field.

## PeerPodNode

PeerPodNode is a genuine Kubernetes Node control path. It runs kubelet,
containerd, the Kata remote shim, and CAA. Workload compute does not run in the
PeerPodNode container: the provider creates one remote PodVM per sandbox.

The image is intentionally not deployed by this repository. A KaaS controller
must run it as a privileged, cluster-scoped service instance and provide:

- `/etc/peer-pod-node/bootstrap-kubeconfig`;
- `/etc/peer-pod-node/cluster-ca.crt`;
- a CNI configuration in `/etc/cni/net.d`;
- the provider endpoint and credentials expected by CAA;
- a workload-cluster credential at the standard in-cluster ServiceAccount
  path, with `KUBERNETES_SERVICE_HOST`/`KUBERNETES_SERVICE_PORT` pointing to
  that workload API rather than the infrastructure cluster;
- `PEER_POD_NODE_CLUSTER_DNS` and, when non-default,
  `PEER_POD_NODE_CLUSTER_DOMAIN`;
- writable/mount-propagated state, cgroup, and network namespace mounts needed
  by kubelet and nested containerd.
- a working system-log socket for the Kata shim (normally the host
  `/run/systemd/journal/dev-log` mounted at the same path).

`PEER_POD_NODE_KUBELET_CONFIG` may point to a complete provider-owned kubelet
configuration instead of using the generated configuration. The image
registers the Node with label `kata.peerpods.io/node=true` and taint
`kata.peerpods.io/node=true:NoSchedule` by default. The upper KaaS
layer must add matching affinity and toleration only to workloads selected for
peer-pod execution.

When PeerPodNode runs as a Pod, disable automatic injection of the
infrastructure cluster ServiceAccount token. CAA must read Pods, referenced
image-pull Secrets, and its PeerPodNode object from the workload cluster and
must patch that Node's extended-resource capacity. Grant only those operations
to a dedicated workload-cluster identity.

Build it locally with:

```sh
make docker-build-peer-pod-node
```

## Controller installation

Build and install the CRDs and controller:

```sh
make test
make docker-build IMG=example.test/caa-kubernetes-provider:dev
make deploy IMG=example.test/caa-kubernetes-provider:dev
kubectl apply -f config/samples/sandbox_v1alpha1_podsandboxclass.yaml
```

Release tags publish multi-architecture controller, CAA, Cloud Hypervisor
runner, PodVM, and PeerPodNode images. `make build-installer` generates the
controller manifest and the versioned sample `PodSandboxClass`.

## Development

```sh
make test
make lint
```

The PeerPodNode container is a privileged systems component. A production KaaS
integration must additionally verify Node join, CNI, Service/DNS,
readiness/liveness/startup probes, logs/exec, restart recovery, drain, and
sandbox garbage collection in an end-to-end environment.

For end-to-end architecture and deployment models, see the
[KaaS integration guide](docs/kaas-integration.md). The
[Kamaji reference manifests](examples/kamaji/README.md) show how those
contracts map to one hosted control plane and one PeerPodNode.

## License

Apache License 2.0. See source headers for copyright details.
