# pulumi-hcloud-kubeadm

A [Pulumi](https://www.pulumi.com/) project in Go to create [Hetzner](https://www.hetzner.com/) instances and install a Kubernetes cluster on them using kubeadm

## Pre-requisites & Supported Versions
- Docker (latest version recommended)
- Hetzner account and API key
- Supported images:
  | name           |    OS           |
  |----------------|-----------------|
  |`ubuntu-24.04`    | Ubuntu 24.04    |
  |`ubuntu-22.04`    | Ubuntu 22.04    |  
  |`centos-7`        | CentOS 7        |
  |`centos-stream-8` | CentOS Stream 8 |
- Kubernetes versions supported (certified working)

  Min: `1.24`
  Max: `1.29`

## How to Run

### Create a volume

Create a local folder on your machine that will be used to store the configuration and generated kubeconfig files. Keep this folder secure since it contains sensitive information.

```shell
mkdir ~/hcloud-cluster
```

### Start the container

Start the container and mount the folder you just created. Pass your Hetzner API Key/Token as an env variable to the container

``` shell
docker run -it \ 
  -v ~/hcloud-cluster:/home/pulumi-hcloud-kubeadm/vars \
  -e HCLOUD_TOKEN=xxxxxxxxxxxxxxxxxx \
  docker.io/samene/pulumi-hcloud-kubeadm:<release>
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
pulumi config set image ubuntu-24.04            # replace with your desired os image
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
      #  custom-https:           # 443 -> 31390    }
      #    source: 8443          # 80 -> 31394     } these are created by default
      #    target: 31345
      #  custom-http:
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
        my-tls:
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

The generated kubeconfig files will be saved to `./vars/` folder and can also be fetched using:

```
PULUMI_CONFIG_PASSPHRASE=xxxxxxx pulumi stack output clusters --show-secrets | jq -r '.[0].<clustername>.kubeconfig' > vars/mykubeconfig
```
replace <clustername> with the name from `topology.yaml`, for example, `central` is the name of the cluster.
