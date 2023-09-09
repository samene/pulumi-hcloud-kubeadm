# pulumi-hcloud-kubeadm

A simple [Pulumi](https://www.pulumi.com/) project in Go to create Hetzner instances and install a kubernetes cluster on them using kubeadm

## Pre-requisites
- Go installed (min 1.18) - [How-to](https://go.dev/doc/install)
- Pulumi installed (latest version recommended) - [How-to](https://www.pulumi.com/docs/install/)
- Ansible installed (latest version recommended)
- Hetzner account and API key
- Supported images - `Ubuntu 22.04` and `CentOS 7`

## How to Run

### Clone Repository

```
git clone git@gitlab.com:samene/pulumi-hcloud-kubeadm.git
cd pulumi-hcloud-kubeadm
``````

### Initialize stack (only once)

```
pulumi stack init dev
```

### Configure Hetzner Settings

Set configuration for compute and networking

```
pulumi config set dataCenter ash-dc1            # replace with your desired datacenter
pulumi config set networkZone us-east           # replace with your desired hcloud network zone
pulumi config set image ubuntu-22.04            # replace with your desired os image (currently only ubuntu-22.04 supported)
pulumi config set bastionFlavor cpx11           # replace with your desired flavor for bastion/NAT node
pulumi config set masterFlavor cpx31            # replace with your desired flavor for clontrol plane nodes
pulumi config set workerFlavor cpx41            # replace with your your desired flavor for worker nodes
pulumi config set lbType lb11                   # replace with your desired flavor forload balancer type
pulumi config set sshUser root                  # replace with ssh user name (usually root)
```

Set configuration for authentication to HCloud server. 

```
pulumi config set hcloud:token XXXXXXXXXXX      # replace with your API token (or set env variable)
```

Set the path of the topology file (relative to current folder, or absolute path)

```
pulumi config set topologyFile topology.yaml
```

### Configure topology

Create a file called `topology.yaml` with following format

```
clusters:
  central:
    kubernetes_version: 1.23     # the highest patch version will be selected automatically
    private_registry: my-docker-registry.com:5000/subpath
    insecure_registries:         # list of docker registries to add to insecure registries
    - "10.90.84.113:5000"    
    load_balancer:
      create: true               # create a load balancer node
      port_mappings:             # target port mappings
        https:
          source: 443
          target: 31390
        http:
          source: 80
          target: 31394
    control_plane:
      node_count: 3              # 1 or 3 (if 3, one Load Balancer will be created)
    worker:
      node_count: 4              # if 0, control plane will be untainted to schedule workloads
    cni: flannel                 # flannel or cilium
  edge-1:
    kubernetes_version: 1.23
    private_registry: my-docker-registry.com:5000/subpath
    insecure_registries: []
    load_balancer:
      create: true
      port_mappings:
        tls:
          source: 15443
          target: 31391
    control_plane:
      node_count: 1
    worker:
      node_count: 0
    cni: flannel
```

### Run

```
pulumi up
```

## Output

```
pulumi stack output clusters
```