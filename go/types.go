package main

import (
	"github.com/pulumi/pulumi-command/sdk/go/command/local"
	"github.com/pulumi/pulumi-hcloud/sdk/go/hcloud"
	"github.com/pulumi/pulumi-tls/sdk/v5/go/tls"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

type K8sCluster struct {
	pulumi.ResourceState
}

type infrastructureConfig struct {
	workerFlavor string
	masterFlavor string
	lbType       string
	image        string
	networkZone  string
	dataCenter   string
	sshUser      string
}

type commonInfra struct {
	privateKey         *tls.PrivateKey
	sshKey             *hcloud.SshKey
	network            *hcloud.Network
	subnet             *hcloud.NetworkSubnet
	ctrlPlaneFirewall  *hcloud.Firewall
	workerFirewall     *hcloud.Firewall
	jumpServerFirewall *hcloud.Firewall
	jumpServer         *hcloud.Server
	bastion            *Node
	bastionSetup       *local.Command
}

type infra struct {
	core *commonInfra

	cpNodes        []*hcloud.Server
	workerNodes    []*hcloud.Server
	loadBal        *hcloud.LoadBalancer
	loadBalTargets []*hcloud.LoadBalancerTarget
	inventory      *Inventory
}

type Inventory struct {
	ClusterName        string
	User               string
	LoadBalancer       *Node
	MasterIPs          []*Node
	WorkerIPs          []*Node
	Cni                string
	Cri                string
	K8sversion         string
	PrivateRegistry    string
	InsecureRegistries []string
	Bastion            *Node
}

type Node struct {
	PrivateIP string
	PublicIP  string
}

type PortMapping struct {
	Source int `yaml:"source"`
	Target int `yaml:"target"`
}

type LoadBalancerDef struct {
	Create       bool                   `yaml:"create"`
	PortMappings map[string]PortMapping `yaml:"port_mappings"`
}

type Cluster struct {
	Cri                string          `yaml:"cri"`
	KubernetesVersion  string          `yaml:"kubernetes_version"`
	PrivateRegistry    string          `yaml:"private_registry,omitempty"`
	InsecureRegistries []string        `yaml:"insecure_registries,omitempty"`
	LoadBalancer       LoadBalancerDef `yaml:"load_balancer,omitempty"`
	Ntp                struct {
		Primary   string `yaml:"primary"`
		Secondary string `yaml:"secondary"`
	} `yaml:"ntp"`
	ControlPlane struct {
		NodeCount int `yaml:"node_count"`
	} `yaml:"control_plane"`
	Worker struct {
		NodeCount int `yaml:"node_count"`
	} `yaml:"worker"`
	Cni string `yaml:"cni"`
}

type Topology struct {
	Clusters map[string]Cluster `yaml:"clusters"`
}

var infraWaitFor []pulumi.Output = make([]pulumi.Output, 0)
