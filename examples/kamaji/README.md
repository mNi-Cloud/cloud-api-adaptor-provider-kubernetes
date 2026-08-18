# Kamaji Kubernetes-on-Kubernetes example

This example creates a workload Kubernetes control plane with Kamaji, joins a
`PeerPodNode` running as an infrastructure-cluster StatefulSet, and runs one
workload Pod through CAA and the Kubernetes Pod Sandbox provider.

It demonstrates the complete control path:

```text
Kamaji workload API
  -> PeerPodNode kubelet/containerd
  -> kata-remote and CAA
  -> Pod Sandbox API
  -> infrastructure runner Pod
  -> Cloud Hypervisor PodVM
```

The example is intended for an isolated development cluster. It is not a
production KaaS installation.

## Prerequisites

The infrastructure Kubernetes cluster must already contain:

- Kamaji and a ready `DataStore` named `default`;
- this project's current CRDs, controller, Pod Sandbox API, and
  `PodSandboxClass`;
- Nodes capable of running the selected runner and PodVM;
- `kubectl` and a free local TCP port `16443` for inspecting the workload API.

The released image is referenced by the StatefulSet manifest. Replace it with
an immutable release tag or digest available to the infrastructure Nodes.

## What the manifests cover

`infrastructure/` contains the Kamaji control plane, a one-node bridge CNI
configuration, and the PeerPodNode StatefulSet. `workload/` contains the
workload-cluster RBAC, a PeerPodNode-compatible kube-proxy, RuntimeClass, and
smoke-test Pod.

The Kamaji kube-proxy add-on is disabled. A nested kube-proxy must not tune the
infrastructure Node's global conntrack sysctls, so the reference sets
`conntrack.maxPerCore` and `conntrack.min` to zero and uses Kamaji's direct
control-plane Service as its API endpoint. A KaaS controller must render that
endpoint and image version for each workload cluster.

The PeerPodNode manifest is intentionally excluded from the infrastructure
Kustomization. Before creating it, the KaaS controller must create
`peer-pods-example/peer-pod-node-credentials` with these keys:

- `bootstrap-kubeconfig`: a short-lived kubelet TLS-bootstrap credential for
  this workload cluster;
- `workload-ca.crt`: the workload API CA used by CAA;
- `workload-token`: a token for the workload-cluster ServiceAccount defined in
  `workload/access.yaml`;
- `provider-token`: a cluster-scoped credential accepted by the Pod Sandbox
  provider API.

The KaaS controller obtains the workload administrator credential from Kamaji,
applies `workload/access.yaml` and `workload/runtime.yaml`, creates the
bootstrap token, and materializes the Secret above. It must never place the
Kamaji administrator credential in PeerPodNode. The infrastructure cluster's
ServiceAccount is not mounted into PeerPodNode.

## Required reconciliation order

1. Apply `infrastructure/kustomization.yaml` and wait for the
   `TenantControlPlane` API.
2. Apply `workload/access.yaml` and `workload/runtime.yaml` through that API.
3. Create and approve kubelet bootstrap credentials according to the workload
   cluster's node-enrollment policy.
4. Create `peer-pod-node-credentials` in the infrastructure cluster.
5. Apply `infrastructure/peer-pod-node.yaml` and wait for its Node to become
   `Ready`.
6. Verify and approve the Node's `kubernetes.io/kubelet-serving` CSR using a
   dedicated serving-certificate approver. Do not approve arbitrary CSRs.
7. Apply `workload/sample.yaml` through the workload API.

The reference StatefulSet uses `OnDelete` updates intentionally. Before an
upgrade or replacement, the KaaS controller must cordon the workload-cluster
Node, drain its Pods, wait for the corresponding `PodSandbox` and runner
resources to disappear, and only then delete the PeerPodNode Pod. A blind
StatefulSet rolling update can terminate the nested runtime while sandboxes
still own network namespaces.

These steps are reconciliation responsibilities, not an installation script.
Implement them in the KaaS controller so retries, credential rotation,
deletion, and status are handled consistently.

## Acceptance criteria

Success requires all of the following:

- the workload Pod becomes Ready on `peer-pod-node`;
- its `runtimeClassName` is `kata-remote`;
- a `PodSandbox` and runner Pod appear in the infrastructure cluster;
- `kubectl logs` returns the application output;
- the Pod's readiness probe succeeds through the CAA VXLAN path;
- `kubectl logs` and `kubectl exec` work through the kubelet serving
  certificate;
- DNS and the `web` ClusterIP Service are reachable from another workload Pod;
- deleting the Pod removes its `PodSandbox`, runner, and PodVM.

Deletion follows the reverse ownership order: workload Pods, PeerPodNode,
cluster credentials, workload control plane, then the infrastructure
namespace. A production controller must wait for sandbox cleanup and retain no
bootstrap or provider credential after cluster deletion.

## Deliberate limitations

- The bridge CNI uses one `/24` range and is only suitable for this one-node
  demonstration. A real KaaS must provide multi-node IPAM, routing, Network
  Policy, and restart recovery.
- The example uses Kamaji's existing `default` datastore. Datastore isolation,
  backup, and lifecycle belong to the KaaS implementation.
- It creates one PeerPodNode and does not demonstrate HA, replacement, drain,
  or autoscaling.
- The provider currently has one configured API bearer token. A multi-tenant
  KaaS must isolate provider instances or add tenant-aware authentication and
  authorization.
- The privileged StatefulSet is a reference wrapper for nested kubelet and
  containerd. It also mounts the host's journald syslog socket required by the
  Kata shim. It is not a hardened production deployment.
