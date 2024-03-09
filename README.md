# pulumi-hcloud-kubeadm

A [Pulumi](https://www.pulumi.com/) project in Go to create Hetzner instances and install a kubernetes cluster on them using kubeadm

## Pre-requisites
- Docker (latest version recommended)
- Hetzner account and API key
- Supported images - `Ubuntu 22.04`, `CentOS 7` and `CentOS Stream 8`

## How to Run

### Create a volume

Create a local folder on your machine that will be used to store the configuration and generated kubeconfig files. Keep this folder secure since it contains sensitive information.

```shell
mkdir ~/hcloud-cluster
```

### Start the container

Start the container and mount the folder you just created in it. Pass your Hetzner API Key/Token as an env variable to the container

``` shell
docker run -it \ 
  -v ~/hcloud-cluster:/home/pulumi-hcloud-kubeadm/vars \
  -e HCLOUD_TOKEN=xxxxxxxxxxxxxxxxxx \
  docker.io/samene/pulumi-hcloud-kubeadm:v1.0.0
```

For the first run, you will be asked to set a password to store the encrypted keys.

``` shell
Created stack 'production'
Enter your passphrase to protect config/secrets:  ****
Re-enter your passphrase to confirm:  ****
pulumi-hcloud-kubeadm@963b9474dc97:~$ 
```

A default configuration will be created at first startup. To setup your own configuration follow the steps below

### Configure Hetzner Settings

Set configuration for compute and networking by running below commands or directly editing the file `./vars/Pulumi.production.yaml`

``` shell
pulumi config set dataCenter ash-dc1            # replace with your desired datacenter
pulumi config set networkZone us-east           # replace with your desired hcloud network zone
pulumi config set image ubuntu-22.04            # replace with your desired os image (ubuntu-22.04 or centos-7 or centos-stream-8)
pulumi config set masterFlavor cpx31            # replace with your desired flavor for clontrol plane nodes
pulumi config set workerFlavor cpx41            # replace with your your desired flavor for worker nodes
pulumi config set lbType lb11                   # replace with your desired flavor forload balancer type
pulumi config set sshUser root                  # replace with ssh user name (usually root)
```

### Configure topology

A sample `topology.yaml` is created in `./vars/` folder. Edit this file as per your configuration.

```yaml
clusters:
  central:
    cri: containerd              # containerd or docker (defaults to containerd)
    cni: flannel                 # flannel or cilium
    kubernetes_version: 1.29     # the highest patch version will be selected automatically
    private_registry: my-docker-registry.com:5000
    insecure_registries:         # list of docker registries to add to insecure registries
    - "10.90.84.113:5000"    
    load_balancer:
      create: true               # create a load balancer node
      #port_mappings:            # any extra target port mappings, other than 80 & 443
      #  https:                  # 31390 -> 443    }
      #    source: 8443          # 31394 -> 80     } these are created by default
      #    target: 31345
      #  http:
      #    source: 8080
      #    target: 31367
    control_plane:
      node_count: 3              # 1 or 3 (if 3, one Load Balancer will be created)
    worker:
      node_count: 4              # if 0, control plane will be untainted to schedule workloads
  edge-1:
    cri: docker
    cni: flannel
    kubernetes_version: 1.28
    private_registry: my-docker-registry.com:5000
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
```

### Run

```
pulumi up
```

## Output

The generated kubeconfig files will be saved to `./vars/` folder and can also be fetched using

```
pulumi stack output clusters
```
