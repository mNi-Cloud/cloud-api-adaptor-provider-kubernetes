# Platform integration guide

This guide explains how to use the project as the peer-pod execution backend
of a Kubernetes platform. PeerPodNode is one optional workload-side component;
it is not the project itself.

## Project value and boundary

The project makes an infrastructure Kubernetes cluster usable as a Cloud API
Adaptor provider. It owns the provider-facing sandbox lifecycle:

1. receive an authenticated CAA create, delete, or status operation;
2. represent the request as a `PodSandbox` resource;
3. reconcile it into a runner Pod on a suitable infrastructure Node;
4. launch one PodVM through the selected VMM runner;
5. return the sandbox address and readiness to CAA;
6. stop and garbage-collect the runner and PodVM when the sandbox is deleted.

This boundary is useful because the same implementation can support:

- a Kubernetes cluster with no customer-managed worker pool;
- a hybrid cluster where ordinary Pods use a worker pool and selected Pods use
  peer-pod sandboxes;
- Kubernetes-on-Kubernetes without converting workload Pods into a
  provider-specific public resource;
- Pod-level VM isolation while avoiding a conventional worker VM beneath every
  sandbox.

The public users of the resulting service keep using the Kubernetes API. The
platform operator, not the application developer, installs and operates this
project.

The project is not a hosted control plane, physical-node autoscaler, tenant
API, billing system, or complete KaaS controller. PeerPodNode is not a Virtual
Kubelet implementation: when selected, it supplies a real kubelet and
containerd path to the provider backend.

## Provider-independent architecture

```text
workload Kubernetes API
        │
        ├─ customer worker Node (optional) ── ordinary container runtime
        │
        └─ PeerPodNode
             kubelet → containerd → kata-remote → CAA
                                                  │
                                                  ▼
                          Kubernetes provider API and controller
                                                  │
                                                  ▼
                                  runner Pod → one remote PodVM
```

The infrastructure side contains three layers:

1. the HTTP Pod Sandbox API receives CAA create/delete operations;
2. the `PodSandbox` controller materializes the request as a runner Pod;
3. the selected runner launches the PodVM and reports its reachable address.

The workload side uses the CAA Kubernetes plugin. PeerPodNode is one way to
host that plugin together with a real kubelet/containerd path; an ordinary
worker Node with CoCo/CAA installed is another. Both use the same provider API,
`PodSandbox` controller, runner contract, and PodVM artifact flow.

## Deployment models

### Add peer pods to an existing cluster

Install the CAA remote runtime on existing workers and deploy the provider
backend in the infrastructure cluster. This is the smallest integration and is
appropriate when eliminating worker pools is not a requirement.

### Hybrid KaaS

Run both ordinary worker Nodes and provider-managed PeerPodNodes. Admission
policy selects peer-pod workloads by namespace/labels. This supports gradual
adoption and mixed Service endpoints while keeping placement explicit.

### Serverless-only KaaS

Create a hosted control plane and at least one provider-managed PeerPodNode as
part of the service instance. Customers do not create worker pools. The
PeerPodNode supplies the Kubernetes Node/CRI control path; runner Pods and
PodVMs are created only when workloads exist.

This is scale-to-zero for workload sandboxes, not for the small managed
PeerPodNode control path. Removing the Node object entirely would require a
Virtual Kubelet-style model and would no longer preserve the same kubelet/CRI
contract.

The hosted control plane may be Kamaji, another hosted-control-plane project,
or a conventional Kubernetes control plane. The only requirement is a normal
Kubernetes node-bootstrap contract and network reachability from PeerPodNode to
the API server and provider endpoint.

## Responsibilities

This project owns compatibility among the PeerPodNode, CAA plugin, provider
API, controller, runner contract, and released images.

The integrating KaaS owns:

1. control-plane creation and deletion;
2. one isolated PeerPodNode identity and state directory per workload cluster;
3. bootstrap credentials, CSR approval/rotation policy, and cluster CA;
4. a workload-cluster identity for CAA's Pod/Secret reads and Node status
   patch;
5. CNI configuration and routes for the workload cluster;
6. the CAA provider endpoint and its bearer credential;
7. scheduling policy, upgrades, drain, replacement, and scaling;
8. tenant-facing status, quotas, billing, and support.

Do not share a kubelet identity or its state across workload clusters.
PeerPodNode replacement must be a controlled operation: cordon, drain, wait
for provider-side sandbox cleanup, then stop and replace the instance. Do not
rely on an ordinary StatefulSet rolling update while workload Pods remain.

## Kamaji example sequence

Kamaji is only an example control-plane provider; PeerPodNode does not import
or call Kamaji APIs.

Reference manifests are available in
[`examples/kamaji`](../examples/kamaji/README.md). They intentionally do not
automate credentials, CSR approval, or lifecycle: those operations belong to
the KaaS controller. The sequence below defines the contracts that controller
must own.

### 1. Create the workload control plane

Create a Kamaji Tenant Control Plane and its datastore using the normal Kamaji
workflow. Wait until its API endpoint is reachable. Retain an administrator
kubeconfig in the KaaS control plane; do not mount that administrator
kubeconfig into PeerPodNode.

### 2. Prepare node bootstrap

Using the workload cluster's supported kubeadm/bootstrap flow, create a
short-lived bootstrap token and a bootstrap kubeconfig for kubelet TLS
bootstrap. Configure the standard RBAC and CSR approval policy appropriate for
managed nodes. Store the bootstrap kubeconfig and cluster CA in a Secret owned
by the KaaS service instance.

PeerPodNode expects these files by default:

```text
/etc/peer-pod-node/bootstrap-kubeconfig
/etc/peer-pod-node/cluster-ca.crt
```

Create a separate workload-cluster credential for CAA. It needs to read the
Pods and image-pull credentials whose sandboxes it serves and patch the
PeerPodNode's extended-resource capacity. It must not receive infrastructure
cluster credentials.

When PeerPodNode itself is an infrastructure-cluster Pod:

- set `automountServiceAccountToken: false`;
- mount the workload credential as
  `/var/run/secrets/kubernetes.io/serviceaccount/token` and its CA as
  `/var/run/secrets/kubernetes.io/serviceaccount/ca.crt`;
- set `KUBERNETES_SERVICE_HOST` and `KUBERNETES_SERVICE_PORT` to the workload
  API endpoint.

CAA uses the standard in-cluster client for these operations. If the
infrastructure ServiceAccount is left mounted, CAA will address the wrong
cluster.

### 3. Install the Kubernetes provider

Install this repository's controller in the infrastructure Kubernetes cluster,
then create a `PodSandboxClass`. The class chooses:

- the runner implementation image;
- the PodVM artifact image;
- placement constraints for hosts that can run PodVMs;
- CNI annotations and runtime asset location.

The default sample uses the Cloud Hypervisor runner. Selecting a future QEMU
or Firecracker runner only changes the class/image; it does not change
PeerPodNode.

### 4. Configure the provider connection

Give the PeerPodNode instance network access to the Pod Sandbox API and set:

```text
KUBERNETES_POD_SANDBOX_ENDPOINT=https://provider.example
KUBERNETES_POD_SANDBOX_TOKEN=<cluster-scoped bearer token>
KUBERNETES_POD_SANDBOX_TIMEOUT=2m
```

The configured bearer token grants access to the provider's default
`PodSandboxClass`. A KaaS controller can grant an individual service instance
access to exactly one other class by creating a Secret in the provider
namespace:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: cluster-a
  namespace: caa-provider-kubernetes-system
  labels:
    sandbox.caa.mnicloud.jp/access: "true"
type: Opaque
stringData:
  token: <cryptographically-random-token>
  className: cluster-a
```

Pass that token only to the corresponding PeerPodNode. The API uses the mapped
class for configuration and sandbox creation, and rejects deletion of a
sandbox owned by another class. The upper KaaS layer owns creation, rotation,
and removal of these per-instance credentials and classes.

### 5. Supply workload networking

Place CNI binaries in `/opt/cni/bin` and configuration in `/etc/cni/net.d`.
Set the cluster DNS address and domain:

```text
PEER_POD_NODE_CLUSTER_DNS=10.96.0.10
PEER_POD_NODE_CLUSTER_DOMAIN=cluster.local
```

These values must come from the workload cluster configuration. They are not
baked into the image. A provider may instead mount a complete kubelet config
and set `PEER_POD_NODE_KUBELET_CONFIG`.

### 6. Start the PeerPodNode service instance

Run the released `peer-pod-node` image as a privileged service instance. It
requires persistent state for its kubelet identity and certificates, writable
runtime state for containerd and CNI, plus the cgroup, mount-propagation, and
network-namespace access required by a nested container runtime. The exact
Pod/VM/process wrapper is a KaaS policy decision, so this repository does not
install one automatically.

Wait for all of the following before reporting the managed cluster ready:

1. PeerPodNode `/readyz` succeeds;
2. its Node object exists and is `Ready`;
3. the Node has `kata.peerpods.io/node=true`;
4. the provider API is reachable with the cluster credential;
5. the workload CNI and DNS path pass a smoke test.

### 7. Select peer-pod workloads

Create a RuntimeClass using handler `kata-remote`. A KaaS admission policy may
add that RuntimeClass, the PeerPodNode affinity, and its toleration to Pods
selected by namespace/labels. It must also request the
`kata.peerpods.io/vm` extended resource so the scheduler observes the capacity
advertised by CAA. Pods outside that policy continue to use an
ordinary worker pool. Do not silently fall back a selected Pod to an ordinary
worker Node.

### 8. Verify the service, not only Pod creation

The integration gate should cover:

- Pod Running and Ready;
- startup, readiness, and liveness probes;
- logs, exec, and deletion;
- DNS and ClusterIP Service access;
- communication between ordinary-worker and peer-pod endpoints in a hybrid
  cluster;
- PeerPodNode restart and sandbox rediscovery;
- drain and replacement without orphaned `PodSandbox` or runner resources.

## Lifecycle guidance

A PeerPodNode represents the managed Kubernetes service's execution control
path, not customer workload capacity. It may exist even when there are no
selected Pods. The demand-created resources are runner Pods and PodVMs.

For a serverless-only cluster, create the PeerPodNode as part of cluster
provisioning so system and user workloads have a real Node path. For a hybrid
cluster, keep it logically separate from customer-managed worker pools. Scale
and high-availability policy belong to the KaaS controller, not to a
`PodSandboxClass` or an individual workload profile.

## Current implementation boundary

The controller/runner path and the Cloud Hypervisor PodVM implementation are
available in this repository. The Kamaji reference exercises one workload
control plane and one PeerPodNode, but it is not evidence of production
readiness. A production integration must separately validate nested
kubelet/containerd recovery, its chosen multi-node CNI, HA, upgrades, drain,
credential rotation, and multi-cluster isolation.
