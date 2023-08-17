package main

import (
	"bytes"
	_ "embed"
	"regexp"
	"text/template"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/config"

	"fmt"
	"os"
	"strconv"

	"github.com/pulumi/pulumi-command/sdk/go/command/local"
	"github.com/pulumi/pulumi-hcloud/sdk/go/hcloud"
	tls "github.com/pulumi/pulumi-tls/sdk/v4/go/tls"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/rs/zerolog/log"

	"gopkg.in/yaml.v2"
)

//go:embed inventory.tmpl
var inventoryTmpl []byte

//go:embed variables.tmpl
var variablesTmpl []byte

func readTopology(filename string) *Topology {
	topology := &Topology{}
	topo, err := os.ReadFile(filename)
	if err != nil {
		log.Fatal().Err(err).Msgf("Cannot open topology file %s ", filename)
	}
	err = yaml.Unmarshal(topo, &topology)
	if err != nil {
		log.Fatal().Err(err).Msgf("Cannot unmarshal topology file %s, is it in correct format?", filename)
	}
	return topology
}

func NewK8sCluster(ctx *pulumi.Context, name string, opts ...pulumi.ResourceOption) (*K8sCluster, error) {
	k8sCluster := &K8sCluster{}
	err := ctx.RegisterComponentResource("pkg:k8s:K8sCluster", name, k8sCluster, opts...)
	if err != nil {
		return nil, err
	}
	return k8sCluster, nil
}

func readConfig(ctx *pulumi.Context) (*infrastructureConfig, *Topology) {
	conf := config.New(ctx, "")
	infraCfg := &infrastructureConfig{}
	infraCfg.workerFlavor = conf.Require("workerFlavor")
	infraCfg.masterFlavor = conf.Require("masterFlavor")
	infraCfg.networkZone = conf.Require("networkZone")
	infraCfg.dataCenter = conf.Require("dataCenter")
	infraCfg.bastionFlavor = conf.Require("bastionFlavor")
	infraCfg.lbType = conf.Require("lbType")
	infraCfg.image = conf.Require("image")
	infraCfg.sshUser = conf.Require("sshUser")
	topologyFile := conf.Require("topologyFile")
	topology := readTopology(topologyFile)
	return infraCfg, topology
}

func main() {
	pulumi.Run(deploy)
}

func deploy(ctx *pulumi.Context) (err error) {
	infraCfg, topology := readConfig(ctx)
	clusterConfigs := make([]interface{}, 0)
	coreInfra := &commonInfra{}
	// private key
	err = setupKeys(ctx, coreInfra)
	if err != nil {
		return
	}
	// network and subnet
	err = setupNetwork(ctx, infraCfg, coreInfra)
	if err != nil {
		return
	}
	// jump server
	err = setupNATAndBastionHost(ctx, infraCfg, coreInfra)
	if err != nil {
		return
	}

	for cName, cluster := range topology.Clusters {
		clusterName := cName
		clusterIterator := cluster
		pulumik8sCluster, err := NewK8sCluster(ctx, clusterName)
		if err != nil {
			return err
		}
		infra := NewClusterInfra(clusterName)
		infra.core = coreInfra

		for instanceIndex := 0; instanceIndex < cluster.ControlPlane.NodeCount; instanceIndex++ {
			// control plane nodes
			masterWorker := cluster.ControlPlane.NodeCount+cluster.Worker.NodeCount <= 1
			err = setupCtrlPlaneNodes(ctx, infraCfg, infra, instanceIndex, clusterName, masterWorker, pulumik8sCluster)
			if err != nil {
				return err
			}
		}
		for instanceIndex := 0; instanceIndex < cluster.Worker.NodeCount; instanceIndex++ {
			// worker nodes
			err = setupWorkerNodes(ctx, infraCfg, infra, instanceIndex, clusterName, pulumik8sCluster)
			if err != nil {
				return err
			}
		}
		if cluster.ControlPlane.NodeCount+cluster.Worker.NodeCount > 1 {
			// create loadbalancer
			err = setupLoadBalancer(ctx, infraCfg, infra, clusterIterator, clusterName, pulumik8sCluster)
			if err != nil {
				return err
			}
		}
		ctx.RegisterResourceOutputs(pulumik8sCluster, pulumi.Map{
			"clusterName": pulumi.String(clusterName),
		})

		// create inventory and run ansible playbooks
		kubeConfig, err := installK8s(ctx, clusterName, infra, pulumik8sCluster)
		if err != nil {
			return err
		}
		clusterConfigs = append(clusterConfigs, kubeConfig)
	}
	output := pulumi.All(clusterConfigs...).ApplyT(func(k []interface{}) []map[string]interface{} {
		clusters := make([]map[string]interface{}, 0)
		for _, kconfig := range k {
			for cname, kc := range kconfig.(map[string]interface{}) {
				entry := make(map[string]interface{}, 0)
				entry[cname] = kc
				clusters = append(clusters, entry)
			}
		}
		return clusters
	}).(pulumi.MapArrayOutput)
	ctx.Export("clusters", pulumi.ToSecret(output))
	ctx.Export("sshkey", coreInfra.privateKey.PrivateKeyOpenssh)
	ctx.Export("jumpserver", coreInfra.jumpServer.Ipv4Address)
	return
}

func installK8s(ctx *pulumi.Context, clusterName string, ictx *infra, pulumik8sCluster *K8sCluster) (config *pulumi.MapOutput, err error) {
	inv, err := local.NewCommand(ctx, fmt.Sprintf("gen-inventory-%s", clusterName), &local.CommandArgs{
		Create: pulumi.All(infraWaitFor).ApplyT(func(notUsed []interface{}) (string, error) {
			// add common bastio
			*ictx.inventory.Bastion = *ictx.core.bastion
			genInventoryFile(ctx, *ictx.inventory)
			return fmt.Sprintf("mv /tmp/inventory-%s.ini ./inventory-%s.ini && mv /tmp/variables-%s.yaml ./variables-%s.yaml && echo \"done\"", clusterName, clusterName, clusterName, clusterName), nil
		}).(pulumi.StringOutput),
		AssetPaths: pulumi.ToStringArray([]string{"inventory-" + clusterName + ".ini"}),
		Delete:     pulumi.StringPtr("rm -rf inventory-" + clusterName + ".ini & rm -rf variables-" + clusterName + ".yaml"),
	}, pulumi.Parent(pulumik8sCluster))
	if err != nil {
		return
	}
	bastionSetup, err := local.NewCommand(ctx, fmt.Sprintf("ansible-setup-nat-%s", clusterName), &local.CommandArgs{
		Create: pulumi.String(fmt.Sprintf("echo \"Waiting 60s...\" && sleep 60 && ANSIBLE_HOST_KEY_CHECKING=False ansible-playbook -i ./inventory-%s.ini ./bastion.yaml -vvv", clusterName)),
		Delete: pulumi.StringPtr("rm -rf cluster-" + clusterName + ".kubeconfig"),
	}, pulumi.DependsOn([]pulumi.Resource{inv}), pulumi.Parent(pulumik8sCluster))
	if err != nil {
		return nil, err
	}
	k8sAnsible, err := local.NewCommand(ctx, fmt.Sprintf("ansible-k8s-installer-%s", clusterName), &local.CommandArgs{
		Create:     pulumi.String(fmt.Sprintf("ANSIBLE_HOST_KEY_CHECKING=False ansible-playbook -i ./inventory-%s.ini -e \"@variables-%s.yaml\" ./install.yaml -vvv", clusterName, clusterName)),
		Delete:     pulumi.StringPtr("rm -rf cluster-" + clusterName + ".kubeconfig"),
		AssetPaths: pulumi.ToStringArray([]string{"cluster-" + clusterName + ".kubeconfig"}),
	}, pulumi.DependsOn([]pulumi.Resource{bastionSetup}), pulumi.Parent(pulumik8sCluster))

	if err != nil {
		return nil, err
	}
	kubeConfig := k8sAnsible.AssetPaths.ApplyT(func(kubeconfigPaths []string) (map[string]interface{}, error) {
		ret := make(map[string]interface{}, 0)
		cConfig := make(map[string]interface{})
		endPointConfig := make(map[string]interface{})
		var kubeConfig string
		kc, err := os.ReadFile(kubeconfigPaths[0])
		if err != nil {
			return nil, nil
		}
		m1 := regexp.MustCompile(`server:.*`)
		if ictx.inventory.LoadBalancer != nil {
			kubeConfig = m1.ReplaceAllString(string(kc), "server: https://"+ictx.inventory.LoadBalancer.PublicIP+":6443")
			endPointConfig["app"] = ictx.inventory.LoadBalancer.PublicIP
			endPointConfig["cluster-api"] = ictx.inventory.LoadBalancer.PublicIP
			endPointConfig["type"] = "LoadBalancer"
		} else {
			if len(ictx.inventory.WorkerIPs) > 0 {
				endPointConfig["app"] = ictx.inventory.WorkerIPs[0].PublicIP
			} else {
				endPointConfig["app"] = ictx.inventory.MasterIPs[0].PublicIP
			}
			endPointConfig["cluster-api"] = ictx.inventory.MasterIPs[0].PublicIP
			kubeConfig = m1.ReplaceAllString(string(kc), "server: https://"+ictx.inventory.MasterIPs[0].PublicIP+":6443")
			endPointConfig["type"] = "NodePort"
		}
		cConfig["endpoints"] = endPointConfig
		cConfig["kubeconfig"] = kubeConfig
		ret[clusterName] = cConfig
		return ret, nil
	}).(pulumi.MapOutput)

	return &kubeConfig, nil
}

func setupLoadBalancer(ctx *pulumi.Context, infraCfg *infrastructureConfig, ictx *infra, c Cluster, clusterName string, pulumik8sCluster *K8sCluster) (err error) {
	ictx.loadBal, err = hcloud.NewLoadBalancer(ctx, fmt.Sprintf("loadBalancer-%s", clusterName), &hcloud.LoadBalancerArgs{
		LoadBalancerType: pulumi.String(infraCfg.lbType),
		NetworkZone:      pulumi.String(infraCfg.networkZone),
	}, pulumi.Parent(pulumik8sCluster))
	if err != nil {
		return
	}
	lbNetwork, err := hcloud.NewLoadBalancerNetwork(ctx, fmt.Sprintf("srvnetwork-%s", clusterName), &hcloud.LoadBalancerNetworkArgs{
		LoadBalancerId: ictx.loadBal.ID().ToStringOutput().ApplyT(strconv.Atoi).(pulumi.IntOutput),
		SubnetId:       ictx.core.subnet.ID(),
	}, pulumi.Parent(pulumik8sCluster))
	if err != nil {
		return
	}
	_, err = hcloud.NewLoadBalancerService(ctx, fmt.Sprintf("lbService-%s-kube-api-6443", clusterName), &hcloud.LoadBalancerServiceArgs{
		LoadBalancerId:  ictx.loadBal.ID(),
		Protocol:        pulumi.String("tcp"),
		DestinationPort: pulumi.Int(6443),
		ListenPort:      pulumi.Int(6443),
	}, pulumi.Parent(pulumik8sCluster))
	if err != nil {
		return
	}
	for name, mapping := range c.LoadBalancer.PortMappings {
		_, err = hcloud.NewLoadBalancerService(ctx, fmt.Sprintf("lbService-%s-%s-%d", clusterName, name, mapping.Source), &hcloud.LoadBalancerServiceArgs{
			LoadBalancerId:  ictx.loadBal.ID(),
			Protocol:        pulumi.String("tcp"),
			DestinationPort: pulumi.Int(mapping.Target),
			ListenPort:      pulumi.Int(mapping.Source),
		}, pulumi.Parent(pulumik8sCluster))
		if err != nil {
			return
		}
	}
	ictx.loadBalTargets = make([]*hcloud.LoadBalancerTarget, 0)
	for i, cpNode := range ictx.cpNodes {
		lbT, err := hcloud.NewLoadBalancerTarget(ctx, fmt.Sprintf("lbtarget-%s-cp-%d", clusterName, i), &hcloud.LoadBalancerTargetArgs{
			Type:           pulumi.String("server"),
			LoadBalancerId: ictx.loadBal.ID().ToStringOutput().ApplyT(strconv.Atoi).(pulumi.IntOutput),
			ServerId:       cpNode.ID().ToStringOutput().ApplyT(strconv.Atoi).(pulumi.IntOutput),
			UsePrivateIp:   pulumi.Bool(true),
		}, pulumi.Parent(pulumik8sCluster))
		if err != nil {
			return err
		}
		ictx.loadBalTargets = append(ictx.loadBalTargets, lbT)
	}
	for i, worker := range ictx.workerNodes {
		lbT, err := hcloud.NewLoadBalancerTarget(ctx, fmt.Sprintf("lbtarget-%s-wrk-%d", clusterName, i), &hcloud.LoadBalancerTargetArgs{
			Type:           pulumi.String("server"),
			LoadBalancerId: ictx.loadBal.ID().ToStringOutput().ApplyT(strconv.Atoi).(pulumi.IntOutput),
			ServerId:       worker.ID().ToStringOutput().ApplyT(strconv.Atoi).(pulumi.IntOutput),
			UsePrivateIp:   pulumi.Bool(true),
		}, pulumi.Parent(pulumik8sCluster))
		if err != nil {
			return err
		}
		ictx.loadBalTargets = append(ictx.loadBalTargets, lbT)
	}
	lb := pulumi.All(lbNetwork.Ip, ictx.loadBal.Ipv4).ApplyT(func(ips []interface{}) []string {
		node := &Node{}
		node.PrivateIP = ips[0].(string)
		node.PublicIP = ips[1].(string)
		ictx.inventory.LoadBalancer = node
		return make([]string, 0)
	}).(pulumi.StringArrayOutput)
	infraWaitFor = append(infraWaitFor, lb)

	return
}

func setupWorkerNodes(ctx *pulumi.Context, infraCfg *infrastructureConfig, ictx *infra, index int, clusterName string, pulumik8sCluster *K8sCluster) (err error) {
	if ictx.workerNodes == nil {
		ictx.workerNodes = make([]*hcloud.Server, 0)
	}
	workerNode, err := hcloud.NewServer(ctx, fmt.Sprintf("worker-%s-%d", clusterName, index), &hcloud.ServerArgs{
		Image:      pulumi.String(infraCfg.image),
		Datacenter: pulumi.String(infraCfg.dataCenter),
		ServerType: pulumi.String(infraCfg.workerFlavor),
		SshKeys:    pulumi.StringArray{ictx.core.sshKey.ID()},
		PublicNets: hcloud.ServerPublicNetArray{hcloud.ServerPublicNetArgs{
			Ipv4Enabled: pulumi.Bool(false),
			Ipv6Enabled: pulumi.Bool(false),
		}},
		Networks: hcloud.ServerNetworkTypeArray{
			hcloud.ServerNetworkTypeArgs{
				NetworkId: ictx.core.subnet.NetworkId,
			}},
		FirewallIds: pulumi.IntArray{
			ictx.core.workerFirewall.ID().ToStringOutput().ApplyT(strconv.Atoi).(pulumi.IntOutput),
		},
	}, pulumi.Parent(pulumik8sCluster))
	ictx.workerNodes = append(ictx.workerNodes, workerNode)
	wn := workerNode.Networks.Index(pulumi.Int(0)).Ip().ApplyT(func(ip *string) string {
		node := &Node{}
		node.PrivateIP = *ip
		// workers will never have public IP
		ictx.inventory.WorkerIPs = append(ictx.inventory.WorkerIPs, node)
		return ""
	})
	infraWaitFor = append(infraWaitFor, wn)
	return
}

func setupCtrlPlaneNodes(ctx *pulumi.Context, infraCfg *infrastructureConfig, ictx *infra, index int, clusterName string, masterWorker bool, pulumik8sCluster *K8sCluster) (err error) {
	if ictx.cpNodes == nil {
		ictx.cpNodes = make([]*hcloud.Server, 0)
	}
	var flavor string
	if masterWorker {
		flavor = infraCfg.workerFlavor
	} else {
		flavor = infraCfg.masterFlavor
	}
	cpNode, err := hcloud.NewServer(ctx, fmt.Sprintf("control-plane-%s-%d", clusterName, index), &hcloud.ServerArgs{
		Image:      pulumi.String(infraCfg.image),
		Datacenter: pulumi.String(infraCfg.dataCenter),
		ServerType: pulumi.String(flavor),
		SshKeys:    pulumi.StringArray{ictx.core.sshKey.ID()},
		PublicNets: hcloud.ServerPublicNetArray{hcloud.ServerPublicNetArgs{
			Ipv4Enabled: pulumi.Bool(masterWorker),
			Ipv6Enabled: pulumi.Bool(false),
		}},
		Networks: hcloud.ServerNetworkTypeArray{
			hcloud.ServerNetworkTypeArgs{
				NetworkId: ictx.core.subnet.NetworkId,
			}},
		FirewallIds: pulumi.IntArray{
			ictx.core.ctrlPlaneFirewall.ID().ToStringOutput().ApplyT(strconv.Atoi).(pulumi.IntOutput),
		},
	}, pulumi.Parent(pulumik8sCluster))
	ictx.cpNodes = append(ictx.cpNodes, cpNode)

	cp := pulumi.All(cpNode.Ipv4Address, cpNode.Networks.Index(pulumi.Int(0)).Ip()).ApplyT(
		func(ips []interface{}) []string {
			node := &Node{}
			node.PrivateIP = *ips[1].(*string)
			node.PublicIP = ips[0].(string)
			ictx.inventory.MasterIPs = append(ictx.inventory.MasterIPs, node)
			return make([]string, 0)
		})

	infraWaitFor = append(infraWaitFor, cp)
	return
}

func setupNATAndBastionHost(ctx *pulumi.Context, infraCfg *infrastructureConfig, coreinfra *commonInfra) (err error) {
	coreinfra.jumpServer, err = hcloud.NewServer(ctx, "jump-server", &hcloud.ServerArgs{
		Image:      pulumi.String(infraCfg.image),
		Datacenter: pulumi.String(infraCfg.dataCenter),
		ServerType: pulumi.String(infraCfg.bastionFlavor),
		SshKeys:    pulumi.StringArray{coreinfra.sshKey.ID()},
		Networks: hcloud.ServerNetworkTypeArray{
			hcloud.ServerNetworkTypeArgs{
				NetworkId: coreinfra.subnet.NetworkId,
			}},
		FirewallIds: pulumi.IntArray{
			coreinfra.jumpServerFirewall.ID().ToStringOutput().ApplyT(strconv.Atoi).(pulumi.IntOutput),
		},
	})
	if err != nil {
		return
	}
	bas := pulumi.All(coreinfra.jumpServer.Networks.Index(pulumi.Int(0)).Ip(), coreinfra.jumpServer.Ipv4Address).ApplyT(
		func(ips []interface{}) []string {
			node := &Node{}
			node.PrivateIP = *ips[0].(*string)
			node.PublicIP = ips[1].(string)
			coreinfra.bastion = node
			return make([]string, 0)
		},
	)
	infraWaitFor = append(infraWaitFor, bas)

	bastionNet, err := hcloud.NewServerNetwork(ctx, "bastion-private-net", &hcloud.ServerNetworkArgs{
		ServerId:  coreinfra.jumpServer.ID().ToStringOutput().ApplyT(strconv.Atoi).(pulumi.IntOutput),
		NetworkId: coreinfra.network.ID().ToStringOutput().ApplyT(strconv.Atoi).(pulumi.IntOutput),
	})
	if err != nil {
		return
	}
	_, err = hcloud.NewNetworkRoute(ctx, "nat-route", &hcloud.NetworkRouteArgs{
		NetworkId:   coreinfra.network.ID().ToStringOutput().ApplyT(strconv.Atoi).(pulumi.IntOutput),
		Destination: pulumi.String("0.0.0.0/0"),
		Gateway:     bastionNet.Ip,
	})
	return
}

func setupKeys(ctx *pulumi.Context, ictx *commonInfra) (err error) {
	ictx.privateKey, err = tls.NewPrivateKey(ctx, "pulumi-hcloud-kubeadm", &tls.PrivateKeyArgs{
		Algorithm: pulumi.String("RSA"),
	})
	if err != nil {
		return
	}
	ictx.privateKey.PrivateKeyOpenssh.ApplyT(func(privateKey string) string {
		os.WriteFile("/tmp/id_rsa", []byte(privateKey), 0600)
		return privateKey
	})
	ictx.sshKey, err = hcloud.NewSshKey(ctx, "pulumi-hcloud-kubeadm", &hcloud.SshKeyArgs{
		PublicKey: ictx.privateKey.PublicKeyOpenssh,
	})

	ictx.sshKey.ToSshKeyOutput()
	return
}

func setupNetwork(ctx *pulumi.Context, infraCfg *infrastructureConfig, ictx *commonInfra) (err error) {
	ictx.network, err = hcloud.NewNetwork(ctx, "kubeadm-network", &hcloud.NetworkArgs{
		IpRange: pulumi.String("10.0.0.0/16"),
	})
	if err != nil {
		return
	}
	ictx.subnet, err = hcloud.NewNetworkSubnet(ctx, "kubeadm-network-subnet", &hcloud.NetworkSubnetArgs{
		NetworkId:   ictx.network.ID().ToStringOutput().ApplyT(strconv.Atoi).(pulumi.IntOutput),
		Type:        pulumi.String("cloud"),
		NetworkZone: pulumi.String(infraCfg.networkZone),
		IpRange:     pulumi.String("10.0.1.0/24"),
	})
	if err != nil {
		return
	}
	ictx.jumpServerFirewall, err = hcloud.NewFirewall(ctx, "jump-server-firewall", &hcloud.FirewallArgs{
		Rules: hcloud.FirewallRuleArray{
			&hcloud.FirewallRuleArgs{
				Direction: pulumi.String("in"),
				Protocol:  pulumi.String("tcp"),
				Port:      pulumi.String("22"),
				SourceIps: pulumi.StringArray{
					pulumi.String("0.0.0.0/0"),
				},
			},
		},
	})
	if err != nil {
		return
	}
	ictx.workerFirewall, err = hcloud.NewFirewall(ctx, "worker-firewall", &hcloud.FirewallArgs{
		Rules: hcloud.FirewallRuleArray{
			&hcloud.FirewallRuleArgs{
				Description: pulumi.String("Kubelet API"),
				Direction:   pulumi.String("in"),
				Protocol:    pulumi.String("tcp"),
				Port:        pulumi.String("10250"),
				SourceIps: pulumi.StringArray{
					pulumi.String("10.0.1.0/24"),
				},
			},
			// nodeports only from loadbalancer
			&hcloud.FirewallRuleArgs{
				Description: pulumi.String("worker-Nodeports"),
				Direction:   pulumi.String("in"),
				Protocol:    pulumi.String("tcp"),
				Port:        pulumi.String("30000-32767"),
				SourceIps: pulumi.StringArray{
					pulumi.String("10.0.1.0/24"),
				},
			},
			// workers can only be ssh'ed from bastion host
			&hcloud.FirewallRuleArgs{
				Direction: pulumi.String("in"),
				Protocol:  pulumi.String("tcp"),
				Port:      pulumi.String("22"),
				SourceIps: pulumi.StringArray{
					pulumi.String("10.0.1.0/24"),
				},
			},
		},
	})
	if err != nil {
		return
	}
	ictx.ctrlPlaneFirewall, err = hcloud.NewFirewall(ctx, "control-plane-firewall", &hcloud.FirewallArgs{
		Rules: hcloud.FirewallRuleArray{
			&hcloud.FirewallRuleArgs{
				Direction: pulumi.String("in"),
				Protocol:  pulumi.String("tcp"),
				Port:      pulumi.String("22"),
				SourceIps: pulumi.StringArray{
					pulumi.String("10.0.1.0/24"),
				},
			},
			// nodeports only from loadbalancer
			&hcloud.FirewallRuleArgs{
				Description: pulumi.String("Ncp-odeports"),
				Direction:   pulumi.String("in"),
				Protocol:    pulumi.String("tcp"),
				Port:        pulumi.String("30000-32767"),
				SourceIps: pulumi.StringArray{
					pulumi.String("10.0.1.0/24"),
				},
			},
			&hcloud.FirewallRuleArgs{
				Description: pulumi.String("Kubernetes API server"),
				Direction:   pulumi.String("in"),
				Protocol:    pulumi.String("tcp"),
				Port:        pulumi.String("6443"),
				SourceIps: pulumi.StringArray{
					pulumi.String("0.0.0.0/0"),
				},
			},
			&hcloud.FirewallRuleArgs{
				Description: pulumi.String("etcd server client API"),
				Direction:   pulumi.String("in"),
				Protocol:    pulumi.String("tcp"),
				Port:        pulumi.String("2379-2380"),
				SourceIps: pulumi.StringArray{
					pulumi.String("10.0.1.0/24"),
				},
			},
			&hcloud.FirewallRuleArgs{
				Description: pulumi.String("Kubelet API"),
				Direction:   pulumi.String("in"),
				Protocol:    pulumi.String("tcp"),
				Port:        pulumi.String("10250"),
				SourceIps: pulumi.StringArray{
					pulumi.String("10.0.1.0/24"),
				},
			},
			&hcloud.FirewallRuleArgs{
				Description: pulumi.String("kube-scheduler"),
				Direction:   pulumi.String("in"),
				Protocol:    pulumi.String("tcp"),
				Port:        pulumi.String("10259"),
				SourceIps: pulumi.StringArray{
					pulumi.String("10.0.1.0/24"),
				},
			},
			&hcloud.FirewallRuleArgs{
				Description: pulumi.String("kube-controller-manager"),
				Direction:   pulumi.String("in"),
				Protocol:    pulumi.String("tcp"),
				Port:        pulumi.String("10257"),
				SourceIps: pulumi.StringArray{
					pulumi.String("10.0.1.0/24"),
				},
			},
			&hcloud.FirewallRuleArgs{
				Description: pulumi.String("flannel"),
				Direction:   pulumi.String("in"),
				Protocol:    pulumi.String("udp"),
				Port:        pulumi.String("8285"),
				SourceIps: pulumi.StringArray{
					pulumi.String("10.0.1.0/24"),
				},
			},
		},
	})
	if err != nil {
		return
	}
	return
}

func genInventoryFile(ctx *pulumi.Context, clusterInventory Inventory) {
	renderedTemplate, parseErr := template.New("invtpl").Parse(string(inventoryTmpl))
	if parseErr != nil {
		ctx.Log.Error("Error parsing template file", nil)
	}
	var buff bytes.Buffer
	if err := renderedTemplate.Execute(&buff, clusterInventory); err != nil {
		ctx.Log.Error("Failed to render inventory template "+err.Error(), nil)
	}

	outFileLoc := fmt.Sprintf("/tmp/inventory-%s.ini", clusterInventory.ClusterName)

	if err := os.WriteFile(outFileLoc, buff.Bytes(), 0655); err != nil {
		ctx.Log.Error("Failed to write inventory files ", nil)
	}

	buff.Reset()
	renderedTemplate, parseErr = template.New("invtpl").Parse(string(variablesTmpl))
	if parseErr != nil {
		ctx.Log.Error("Error parsing template file ", nil)
	}
	if err := renderedTemplate.Execute(&buff, clusterInventory); err != nil {
		ctx.Log.Error("Failed to render inventory template "+err.Error(), nil)
	}

	outFileLoc = fmt.Sprintf("/tmp/variables-%s.yaml", clusterInventory.ClusterName)

	if err := os.WriteFile(outFileLoc, buff.Bytes(), 0655); err != nil {
		ctx.Log.Error("Failed to write inventory files "+err.Error(), nil)
	}

}

func NewClusterInfra(clusterName string) *infra {
	workerIps := make([]*Node, 0)
	cpIps := make([]*Node, 0)

	inv := &Inventory{Cni: "flannel",
		K8sversion:  "1.23.17-00",
		User:        "root",
		WorkerIPs:   workerIps,
		MasterIPs:   cpIps,
		Bastion:     &Node{},
		ClusterName: clusterName}
	i := &infra{inventory: inv}
	return i
}
