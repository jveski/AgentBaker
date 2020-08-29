// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT license.

package datamodel

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"

	"github.com/Azure/go-autorest/autorest/to"

	"github.com/Azure/agentbaker/pkg/aks-engine/helpers"
	"github.com/Azure/aks-engine/pkg/api/common"
	log "github.com/sirupsen/logrus"
)

// DistroValues is a list of currently supported distros
var DistroValues = []Distro{"", Ubuntu, Ubuntu1804, AKSUbuntu1604, AKSUbuntu1804, Ubuntu1804Gen2, AKSUbuntuGPU1804, AKSUbuntuGPU1804Gen2}

// PropertiesDefaultsParams is the parameters when we set the properties defaults for ContainerService.
type PropertiesDefaultsParams struct {
	IsUpgrade  bool
	IsScale    bool
	PkiKeySize int
}

// SetPropertiesDefaults for the container Properties
func (cs *ContainerService) SetPropertiesDefaults(params PropertiesDefaultsParams, cloudSpecConfig *AzureEnvironmentSpecConfig) error {
	// Set master profile defaults if this cluster configuration includes master node(s)
	if cs.Properties.MasterProfile != nil {
		cs.setMasterProfileDefaults(params.IsUpgrade)
	}

	// move load balancer sku defaults logic from setOrchestratorDefaults() to here to serve LB checking at setAgentProfileDefaults()
	cs.setLoadBalancerSkuDefaults()

	cs.setAgentProfileDefaults(params.IsUpgrade, params.IsScale)

	cs.setStorageDefaults()
	cs.setOrchestratorDefaults(params.IsUpgrade, params.IsScale, cloudSpecConfig)
	cs.setExtensionDefaults()

	// Set hosted master profile defaults if this cluster configuration has a hosted control plane
	if cs.Properties.HostedMasterProfile != nil {
		cs.setHostedMasterProfileDefaults()
	}

	if cs.Properties.WindowsProfile != nil {
		cs.setWindowsProfileDefaults(params.IsUpgrade, params.IsScale)
	}

	cs.setTelemetryProfileDefaults()

	return nil
}

// setOrchestratorDefaults for orchestrators
func (cs *ContainerService) setOrchestratorDefaults(isUpgrade, isScale bool, cloudSpecConfig *AzureEnvironmentSpecConfig) {
	isUpdate := isUpgrade || isScale
	a := cs.Properties

	if a.OrchestratorProfile == nil {
		return
	}
	o := a.OrchestratorProfile
	if o.OrchestratorVersion == "" {
		o.OrchestratorVersion = common.GetValidPatchVersion(
			o.OrchestratorType,
			o.OrchestratorVersion, isUpdate, a.HasWindows())
	}

	switch o.OrchestratorType {
	case Kubernetes:
		if o.KubernetesConfig == nil {
			o.KubernetesConfig = &KubernetesConfig{}
		}
		// For backwards compatibility with original, overloaded "NetworkPolicy" config vector
		// we translate deprecated NetworkPolicy usage to the NetworkConfig equivalent
		// and set a default network policy enforcement configuration
		switch o.KubernetesConfig.NetworkPolicy {
		case NetworkPolicyAzure:
			if o.KubernetesConfig.NetworkPlugin == "" {
				o.KubernetesConfig.NetworkPlugin = NetworkPluginAzure
				o.KubernetesConfig.NetworkPolicy = DefaultNetworkPolicy
			}
		case NetworkPolicyNone:
			o.KubernetesConfig.NetworkPlugin = NetworkPluginKubenet
			o.KubernetesConfig.NetworkPolicy = DefaultNetworkPolicy
		case NetworkPolicyCalico:
			if o.KubernetesConfig.NetworkPlugin == "" {
				// If not specified, then set the network plugin to be kubenet
				// for backwards compatibility. Otherwise, use what is specified.
				o.KubernetesConfig.NetworkPlugin = NetworkPluginKubenet
			}
		case NetworkPolicyCilium:
			o.KubernetesConfig.NetworkPlugin = NetworkPluginCilium
		case NetworkPolicyAntrea:
			o.KubernetesConfig.NetworkPlugin = NetworkPluginAntrea
		}

		if o.KubernetesConfig.KubernetesImageBase == "" {
			o.KubernetesConfig.KubernetesImageBase = cloudSpecConfig.KubernetesSpecConfig.KubernetesImageBase
		}

		if o.KubernetesConfig.MCRKubernetesImageBase == "" {
			o.KubernetesConfig.MCRKubernetesImageBase = cloudSpecConfig.KubernetesSpecConfig.MCRKubernetesImageBase
		}

		if o.KubernetesConfig.EtcdVersion == "" {
			o.KubernetesConfig.EtcdVersion = DefaultEtcdVersion
		} else if isUpgrade {
			if o.KubernetesConfig.EtcdVersion != DefaultEtcdVersion {
				// Override (i.e., upgrade) the etcd version if the default is newer in an upgrade scenario
				if common.GetMinVersion([]string{o.KubernetesConfig.EtcdVersion, DefaultEtcdVersion}, true) == o.KubernetesConfig.EtcdVersion {
					log.Warnf("etcd will be upgraded to version %s\n", DefaultEtcdVersion)
					o.KubernetesConfig.EtcdVersion = DefaultEtcdVersion
				}
			}

		}

		if a.HasWindows() {
			if o.KubernetesConfig.NetworkPlugin == "" {
				o.KubernetesConfig.NetworkPlugin = DefaultNetworkPluginWindows
			}
		} else {
			if o.KubernetesConfig.NetworkPlugin == "" {
				if o.KubernetesConfig.IsAddonEnabled(common.FlannelAddonName) {
					o.KubernetesConfig.NetworkPlugin = NetworkPluginFlannel
				} else {
					o.KubernetesConfig.NetworkPlugin = DefaultNetworkPlugin
				}
			}
		}
		if o.KubernetesConfig.ContainerRuntime == "" {
			o.KubernetesConfig.ContainerRuntime = DefaultContainerRuntime
		}
		switch o.KubernetesConfig.ContainerRuntime {
		case Docker:
			if o.KubernetesConfig.MobyVersion == "" || isUpdate {
				if o.KubernetesConfig.MobyVersion != DefaultMobyVersion {
					if isUpgrade {
						log.Warnf("Moby will be upgraded to version %s\n", DefaultMobyVersion)
					} else if isScale {
						log.Warnf("Any new nodes will have Moby version %s\n", DefaultMobyVersion)
					}
				}
				o.KubernetesConfig.MobyVersion = DefaultMobyVersion
			}
		case Containerd, KataContainers:
			if o.KubernetesConfig.ContainerdVersion == "" || isUpdate {
				if o.KubernetesConfig.ContainerdVersion != DefaultContainerdVersion {
					if isUpgrade {
						log.Warnf("containerd will be upgraded to version %s\n", DefaultContainerdVersion)
					} else if isScale {
						log.Warnf("Any new nodes will have containerd version %s\n", DefaultContainerdVersion)
					}
				}
				o.KubernetesConfig.ContainerdVersion = DefaultContainerdVersion
			}
		}
		if o.KubernetesConfig.ClusterSubnet == "" {
			if o.IsAzureCNI() {
				// When Azure CNI is enabled, all masters, agents and pods share the same large subnet.
				// Except when master is VMSS, then masters and agents have separate subnets within the same large subnet.
				o.KubernetesConfig.ClusterSubnet = DefaultKubernetesSubnet
			} else {
				o.KubernetesConfig.ClusterSubnet = DefaultKubernetesClusterSubnet
				// ipv6 only cluster
				if cs.Properties.FeatureFlags.IsFeatureEnabled("EnableIPv6Only") {
					o.KubernetesConfig.ClusterSubnet = DefaultKubernetesClusterSubnetIPv6
				}
				// ipv4 and ipv6 subnet for dual stack
				if cs.Properties.FeatureFlags.IsFeatureEnabled("EnableIPv6DualStack") {
					o.KubernetesConfig.ClusterSubnet = strings.Join([]string{DefaultKubernetesClusterSubnet, cs.getDefaultKubernetesClusterSubnetIPv6()}, ",")
				}
			}
		} else {
			// ensure 2 subnets exists if ipv6 dual stack feature is enabled
			if cs.Properties.FeatureFlags.IsFeatureEnabled("EnableIPv6DualStack") && !o.IsAzureCNI() {
				clusterSubnets := strings.Split(o.KubernetesConfig.ClusterSubnet, ",")
				if len(clusterSubnets) == 1 {
					// if error exists, then it'll be caught by validate
					ip, _, err := net.ParseCIDR(clusterSubnets[0])
					if err == nil {
						if ip.To4() != nil {
							// the first cidr block is ipv4, so append ipv6
							clusterSubnets = append(clusterSubnets, cs.getDefaultKubernetesClusterSubnetIPv6())
						} else {
							// first cidr has to be ipv4
							clusterSubnets = append([]string{DefaultKubernetesClusterSubnet}, clusterSubnets...)
						}
						// only set the cluster subnet if no error has been encountered
						o.KubernetesConfig.ClusterSubnet = strings.Join(clusterSubnets, ",")
					}
				}
			}
		}
		if o.KubernetesConfig.GCHighThreshold == 0 {
			o.KubernetesConfig.GCHighThreshold = DefaultKubernetesGCHighThreshold
		}
		if o.KubernetesConfig.GCLowThreshold == 0 {
			o.KubernetesConfig.GCLowThreshold = DefaultKubernetesGCLowThreshold
		}
		if o.KubernetesConfig.DNSServiceIP == "" {
			o.KubernetesConfig.DNSServiceIP = DefaultKubernetesDNSServiceIP
			if cs.Properties.FeatureFlags.IsFeatureEnabled("EnableIPv6Only") {
				o.KubernetesConfig.DNSServiceIP = DefaultKubernetesDNSServiceIPv6
			}
		}
		if o.KubernetesConfig.DockerBridgeSubnet == "" {
			o.KubernetesConfig.DockerBridgeSubnet = DefaultDockerBridgeSubnet
		}
		if o.KubernetesConfig.ServiceCIDR == "" {
			o.KubernetesConfig.ServiceCIDR = DefaultKubernetesServiceCIDR
			if cs.Properties.FeatureFlags.IsFeatureEnabled("EnableIPv6Only") {
				o.KubernetesConfig.ServiceCIDR = DefaultKubernetesServiceCIDRIPv6
			}
		}

		if common.IsKubernetesVersionGe(o.OrchestratorVersion, "1.14.0") {
			o.KubernetesConfig.CloudProviderBackoffMode = CloudProviderBackoffModeV2
			if o.KubernetesConfig.CloudProviderBackoff == nil {
				o.KubernetesConfig.CloudProviderBackoff = to.BoolPtr(true)
			}
		} else {
			o.KubernetesConfig.CloudProviderBackoffMode = "v1"
			if o.KubernetesConfig.CloudProviderBackoff == nil {
				o.KubernetesConfig.CloudProviderBackoff = to.BoolPtr(false)
			}
		}

		// Enforce sane cloudprovider backoff defaults.
		o.KubernetesConfig.SetCloudProviderBackoffDefaults()

		if o.KubernetesConfig.CloudProviderRateLimit == nil {
			o.KubernetesConfig.CloudProviderRateLimit = to.BoolPtr(DefaultKubernetesCloudProviderRateLimit)
		}
		// Enforce sane cloudprovider rate limit defaults.
		a.SetCloudProviderRateLimitDefaults()

		if o.KubernetesConfig.PrivateCluster == nil {
			o.KubernetesConfig.PrivateCluster = &PrivateCluster{}
		}

		if o.KubernetesConfig.PrivateCluster.Enabled == nil {
			o.KubernetesConfig.PrivateCluster.Enabled = to.BoolPtr(DefaultPrivateClusterEnabled)
		}

		if o.KubernetesConfig.PrivateCluster.EnableHostsConfigAgent == nil {
			o.KubernetesConfig.PrivateCluster.EnableHostsConfigAgent = to.BoolPtr(DefaultPrivateClusterHostsConfigAgentEnabled)
		}

		if "" == a.OrchestratorProfile.KubernetesConfig.EtcdDiskSizeGB {
			switch {
			case a.TotalNodes() > 20:
				a.OrchestratorProfile.KubernetesConfig.EtcdDiskSizeGB = DefaultEtcdDiskSizeGT20Nodes
			case a.TotalNodes() > 10:
				a.OrchestratorProfile.KubernetesConfig.EtcdDiskSizeGB = DefaultEtcdDiskSizeGT10Nodes
			case a.TotalNodes() > 3:
				a.OrchestratorProfile.KubernetesConfig.EtcdDiskSizeGB = DefaultEtcdDiskSizeGT3Nodes
			default:
				a.OrchestratorProfile.KubernetesConfig.EtcdDiskSizeGB = DefaultEtcdDiskSize
			}
		}

		if to.Bool(o.KubernetesConfig.EnableDataEncryptionAtRest) {
			if "" == a.OrchestratorProfile.KubernetesConfig.EtcdEncryptionKey {
				a.OrchestratorProfile.KubernetesConfig.EtcdEncryptionKey = generateEtcdEncryptionKey()
			}
		}

		if a.OrchestratorProfile.KubernetesConfig.PrivateJumpboxProvision() && a.OrchestratorProfile.KubernetesConfig.PrivateCluster.JumpboxProfile.OSDiskSizeGB == 0 {
			a.OrchestratorProfile.KubernetesConfig.PrivateCluster.JumpboxProfile.OSDiskSizeGB = DefaultJumpboxDiskSize
		}

		if a.OrchestratorProfile.KubernetesConfig.PrivateJumpboxProvision() && a.OrchestratorProfile.KubernetesConfig.PrivateCluster.JumpboxProfile.Username == "" {
			a.OrchestratorProfile.KubernetesConfig.PrivateCluster.JumpboxProfile.Username = DefaultJumpboxUsername
		}

		if a.OrchestratorProfile.KubernetesConfig.PrivateJumpboxProvision() && a.OrchestratorProfile.KubernetesConfig.PrivateCluster.JumpboxProfile.StorageProfile == "" {
			a.OrchestratorProfile.KubernetesConfig.PrivateCluster.JumpboxProfile.StorageProfile = ManagedDisks
		}

		if a.OrchestratorProfile.KubernetesConfig.EnableRbac == nil {
			a.OrchestratorProfile.KubernetesConfig.EnableRbac = to.BoolPtr(DefaultRBACEnabled)
		}

		// Upgrade scenario:
		// We need to force set EnableRbac to true for upgrades to 1.15.0 and greater if it was previously set to false (AKS Engine only)
		if !a.OrchestratorProfile.KubernetesConfig.IsRBACEnabled() && common.IsKubernetesVersionGe(o.OrchestratorVersion, "1.15.0") && isUpgrade && !cs.Properties.IsHostedMasterProfile() {
			log.Warnf("RBAC will be enabled during upgrade to version %s\n", o.OrchestratorVersion)
			a.OrchestratorProfile.KubernetesConfig.EnableRbac = to.BoolPtr(true)
		}

		if a.OrchestratorProfile.KubernetesConfig.IsRBACEnabled() {
			a.OrchestratorProfile.KubernetesConfig.EnableAggregatedAPIs = true
		} else if isUpdate && a.OrchestratorProfile.KubernetesConfig.EnableAggregatedAPIs {
			// Upgrade scenario:
			// We need to force set EnableAggregatedAPIs to false if RBAC was previously disabled
			a.OrchestratorProfile.KubernetesConfig.EnableAggregatedAPIs = false
		}

		if a.OrchestratorProfile.KubernetesConfig.EnableSecureKubelet == nil {
			a.OrchestratorProfile.KubernetesConfig.EnableSecureKubelet = to.BoolPtr(DefaultSecureKubeletEnabled)
		}

		if a.OrchestratorProfile.KubernetesConfig.UseInstanceMetadata == nil {
			a.OrchestratorProfile.KubernetesConfig.UseInstanceMetadata = to.BoolPtr(DefaultUseInstanceMetadata)
		}

		if a.OrchestratorProfile.KubernetesConfig.LoadBalancerSku == StandardLoadBalancerSku && a.OrchestratorProfile.KubernetesConfig.ExcludeMasterFromStandardLB == nil {
			a.OrchestratorProfile.KubernetesConfig.ExcludeMasterFromStandardLB = to.BoolPtr(DefaultExcludeMasterFromStandardLB)
		}

		if a.OrchestratorProfile.IsAzureCNI() {
			if a.HasWindows() {
				a.OrchestratorProfile.KubernetesConfig.AzureCNIVersion = AzureCniPluginVerWindows
			} else {
				a.OrchestratorProfile.KubernetesConfig.AzureCNIVersion = AzureCniPluginVerLinux
			}
		}

		if a.OrchestratorProfile.KubernetesConfig.MaximumLoadBalancerRuleCount == 0 {
			a.OrchestratorProfile.KubernetesConfig.MaximumLoadBalancerRuleCount = DefaultMaximumLoadBalancerRuleCount
		}
		if a.OrchestratorProfile.KubernetesConfig.ProxyMode == "" {
			a.OrchestratorProfile.KubernetesConfig.ProxyMode = DefaultKubeProxyMode
		}
		if a.OrchestratorProfile.KubernetesConfig.LoadBalancerSku == StandardLoadBalancerSku &&
			a.OrchestratorProfile.KubernetesConfig.OutboundRuleIdleTimeoutInMinutes == 0 {
			a.OrchestratorProfile.KubernetesConfig.OutboundRuleIdleTimeoutInMinutes = DefaultOutboundRuleIdleTimeoutInMinutes
		}

		if o.KubernetesConfig.LoadBalancerSku == StandardLoadBalancerSku {
			if o.KubernetesConfig.CloudProviderDisableOutboundSNAT == nil {
				o.KubernetesConfig.CloudProviderDisableOutboundSNAT = to.BoolPtr(false)
			}
		} else {
			// CloudProviderDisableOutboundSNAT is only valid in the context of Standard LB, statically set to false if not Standard LB
			o.KubernetesConfig.CloudProviderDisableOutboundSNAT = to.BoolPtr(false)
		}

		if o.KubernetesConfig.ContainerRuntimeConfig == nil {
			o.KubernetesConfig.ContainerRuntimeConfig = make(map[string]string)
		}

		// Master-specific defaults that depend upon OrchestratorProfile defaults
		if cs.Properties.OrchestratorProfile.KubernetesConfig.LoadBalancerSku == StandardLoadBalancerSku {
			cs.Properties.OrchestratorProfile.KubernetesConfig.ExcludeMasterFromStandardLB = to.BoolPtr(DefaultExcludeMasterFromStandardLB)
		}
		if cs.Properties.MasterProfile != nil {
			if !cs.Properties.MasterProfile.IsCustomVNET() {
				if cs.Properties.OrchestratorProfile.IsAzureCNI() {
					// When VNET integration is enabled, all masters, agents and pods share the same large subnet.
					cs.Properties.MasterProfile.Subnet = cs.Properties.OrchestratorProfile.KubernetesConfig.ClusterSubnet
					// FirstConsecutiveStaticIP is not reset if it is upgrade and some value already exists
					if !isUpgrade || len(cs.Properties.MasterProfile.FirstConsecutiveStaticIP) == 0 {
						if cs.Properties.MasterProfile.IsVirtualMachineScaleSets() {
							cs.Properties.MasterProfile.FirstConsecutiveStaticIP = DefaultFirstConsecutiveKubernetesStaticIPVMSS
							cs.Properties.MasterProfile.Subnet = DefaultKubernetesMasterSubnet
							cs.Properties.MasterProfile.AgentSubnet = DefaultKubernetesAgentSubnetVMSS
						} else {
							cs.Properties.MasterProfile.FirstConsecutiveStaticIP = cs.Properties.MasterProfile.GetFirstConsecutiveStaticIPAddress(cs.Properties.MasterProfile.Subnet)
						}
					}
				} else {
					cs.Properties.MasterProfile.Subnet = DefaultKubernetesMasterSubnet
					cs.Properties.MasterProfile.SubnetIPv6 = DefaultKubernetesMasterSubnetIPv6
					// FirstConsecutiveStaticIP is not reset if it is upgrade and some value already exists
					if !isUpgrade || len(cs.Properties.MasterProfile.FirstConsecutiveStaticIP) == 0 {
						if cs.Properties.MasterProfile.IsVirtualMachineScaleSets() {
							cs.Properties.MasterProfile.FirstConsecutiveStaticIP = DefaultFirstConsecutiveKubernetesStaticIPVMSS
							cs.Properties.MasterProfile.AgentSubnet = DefaultKubernetesAgentSubnetVMSS
						} else {
							cs.Properties.MasterProfile.FirstConsecutiveStaticIP = DefaultFirstConsecutiveKubernetesStaticIP
						}
					}
				}
			}

			// Distro assignment for masterProfile
			if cs.Properties.MasterProfile.Distro == "" && cs.Properties.MasterProfile.ImageRef == nil {
				if cs.Properties.OrchestratorProfile.IsKubernetes() && cs.Properties.OrchestratorProfile.KubernetesConfig.CustomHyperkubeImage == "" {
					cs.Properties.MasterProfile.Distro = AKSUbuntu1604
				} else {
					cs.Properties.MasterProfile.Distro = Ubuntu
				}
			}
			// The AKS Distro is not available in Azure German Cloud.
			if cloudSpecConfig.CloudName == AzureGermanCloud {
				cs.Properties.MasterProfile.Distro = Ubuntu
			}
		}

		// Pool-specific defaults that depend upon OrchestratorProfile defaults
		for _, profile := range cs.Properties.AgentPoolProfiles {
			if cs.Properties.OrchestratorProfile.KubernetesConfig.LoadBalancerSku == StandardLoadBalancerSku {
				cs.Properties.OrchestratorProfile.KubernetesConfig.ExcludeMasterFromStandardLB = to.BoolPtr(DefaultExcludeMasterFromStandardLB)
			}
			// configure the subnets if not in custom VNET
			if cs.Properties.MasterProfile != nil && !cs.Properties.MasterProfile.IsCustomVNET() {
				subnetCounter := 0
				for _, profile := range cs.Properties.AgentPoolProfiles {
					if !cs.Properties.MasterProfile.IsVirtualMachineScaleSets() {
						profile.Subnet = cs.Properties.MasterProfile.Subnet
					}
					if cs.Properties.OrchestratorProfile.OrchestratorType == Kubernetes {
						if !cs.Properties.MasterProfile.IsVirtualMachineScaleSets() {
							profile.Subnet = cs.Properties.MasterProfile.Subnet
						}
					} else {
						profile.Subnet = fmt.Sprintf(DefaultAgentSubnetTemplate, subnetCounter)
					}
					subnetCounter++
				}
			}
			// Distro assignment for pools
			if profile.OSType != Windows {
				if profile.Distro == "" && profile.ImageRef == nil {
					if cs.Properties.OrchestratorProfile.IsKubernetes() && cs.Properties.OrchestratorProfile.KubernetesConfig != nil && cs.Properties.OrchestratorProfile.KubernetesConfig.CustomHyperkubeImage == "" {
						if profile.OSDiskSizeGB != 0 && profile.OSDiskSizeGB < VHDDiskSizeAKS {
							profile.Distro = Ubuntu
						} else {
							profile.Distro = AKSUbuntu1604
						}
					} else {
						profile.Distro = Ubuntu
					}
					// Ensure deprecated distros are overridden
					// Previous versions of aks-engine required the docker-engine distro for N series vms,
					// so we need to hard override it in order to produce a working cluster in upgrade/scale contexts.
				}
				// The AKS Distro is not available in Azure German Cloud.
				if cloudSpecConfig.CloudName == AzureGermanCloud {
					profile.Distro = Ubuntu
				}
			}
		}

		// Configure kubelet
		cs.setKubeletConfig(isUpgrade)

		// Master-specific defaults that depend upon kubelet defaults
		// Set the default number of IP addresses allocated for masters.
		if cs.Properties.MasterProfile != nil {
			if cs.Properties.MasterProfile.IPAddressCount == 0 {
				// Allocate one IP address for the node.
				cs.Properties.MasterProfile.IPAddressCount = 1
				// Allocate IP addresses for pods if VNET integration is enabled.
				if cs.Properties.OrchestratorProfile.IsAzureCNI() {
					masterMaxPods, _ := strconv.Atoi(cs.Properties.MasterProfile.KubernetesConfig.KubeletConfig["--max-pods"])
					cs.Properties.MasterProfile.IPAddressCount += masterMaxPods
				}
			}
		}
		// Pool-specific defaults that depend upon kubelet defaults
		for _, profile := range cs.Properties.AgentPoolProfiles {
			// Set the default number of IP addresses allocated for agents.
			if profile.IPAddressCount == 0 {
				// Allocate one IP address for the node.
				profile.IPAddressCount = 1
				// Allocate IP addresses for pods if VNET integration is enabled.
				if cs.Properties.OrchestratorProfile.IsAzureCNI() {
					agentPoolMaxPods, _ := strconv.Atoi(profile.KubernetesConfig.KubeletConfig["--max-pods"])
					profile.IPAddressCount += agentPoolMaxPods
				}
			}
		}

		// Configure apiserver
		cs.setAPIServerConfig()
	}
}

// getNewIP returns a new IP derived from an address plus a multiple of an offset
func getNewAddr(addr uint32, count int, offsetMultiplier int) uint32 {
	offset := count * offsetMultiplier
	newAddr := addr + uint32(offset)
	return newAddr
}

// certsAlreadyPresent already present returns a map where each key is a type of cert and each value is true if that cert/key pair is user-provided
func certsAlreadyPresent(c *CertificateProfile, m int) map[string]bool {
	g := map[string]bool{
		"ca":         false,
		"apiserver":  false,
		"kubeconfig": false,
		"client":     false,
		"etcd":       false,
	}
	if c != nil {
		etcdPeer := true
		if len(c.EtcdPeerCertificates) != m || len(c.EtcdPeerPrivateKeys) != m {
			etcdPeer = false
		} else {
			for i, p := range c.EtcdPeerCertificates {
				if !(len(p) > 0) || !(len(c.EtcdPeerPrivateKeys[i]) > 0) {
					etcdPeer = false
				}
			}
		}
		g["ca"] = len(c.CaCertificate) > 0 && len(c.CaPrivateKey) > 0
		g["apiserver"] = len(c.APIServerCertificate) > 0 && len(c.APIServerPrivateKey) > 0
		g["kubeconfig"] = len(c.KubeConfigCertificate) > 0 && len(c.KubeConfigPrivateKey) > 0
		g["client"] = len(c.ClientCertificate) > 0 && len(c.ClientPrivateKey) > 0
		g["etcd"] = etcdPeer && len(c.EtcdClientCertificate) > 0 && len(c.EtcdClientPrivateKey) > 0 && len(c.EtcdServerCertificate) > 0 && len(c.EtcdServerPrivateKey) > 0
	}
	return g
}

// combine user-provided --feature-gates vals with defaults
// a minimum k8s version may be declared as required for defaults assignment
func addDefaultFeatureGates(m map[string]string, version string, minVersion string, defaults string) {
	if minVersion != "" {
		if common.IsKubernetesVersionGe(version, minVersion) {
			m["--feature-gates"] = combineValues(m["--feature-gates"], defaults)
		} else {
			m["--feature-gates"] = combineValues(m["--feature-gates"], "")
		}
	} else {
		m["--feature-gates"] = combineValues(m["--feature-gates"], defaults)
	}
}

func combineValues(inputs ...string) string {
	valueMap := make(map[string]string)
	for _, input := range inputs {
		applyValueStringToMap(valueMap, input)
	}
	return mapToString(valueMap)
}

func applyValueStringToMap(valueMap map[string]string, input string) {
	values := strings.Split(input, ",")
	for index := 0; index < len(values); index++ {
		// trim spaces (e.g. if the input was "foo=true, bar=true" - we want to drop the space after the comma)
		value := strings.Trim(values[index], " ")
		valueParts := strings.Split(value, "=")
		if len(valueParts) == 2 {
			valueMap[valueParts[0]] = valueParts[1]
		}
	}
}

func mapToString(valueMap map[string]string) string {
	// Order by key for consistency
	keys := []string{}
	for key := range valueMap {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var buf bytes.Buffer
	for _, key := range keys {
		buf.WriteString(fmt.Sprintf("%s=%s,", key, valueMap[key]))
	}
	return strings.TrimSuffix(buf.String(), ",")
}

func generateEtcdEncryptionKey() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.StdEncoding.EncodeToString(b)
}

// getDefaultKubernetesClusterSubnetIPv6 returns the default IPv6 cluster subnet
func (cs *ContainerService) getDefaultKubernetesClusterSubnetIPv6() string {
	o := cs.Properties.OrchestratorProfile
	// In 1.17+ the default IPv6 mask size is /64 which means the cluster
	// subnet mask size >= /48
	if common.IsKubernetesVersionGe(o.OrchestratorVersion, "1.17.0") {
		return DefaultKubernetesClusterSubnetIPv6
	}
	// In 1.16, the default mask size for IPv6 is /24 which forces the cluster
	// subnet mask size to be strictly >= /8
	return "fc00::/8"
}

func (cs *ContainerService) setMasterProfileDefaults(isUpgrade bool) {
	p := cs.Properties

	// set default to VMAS for now
	if p.MasterProfile.AvailabilityProfile == "" {
		p.MasterProfile.AvailabilityProfile = AvailabilitySet
	}

	if p.MasterProfile.IsVirtualMachineScaleSets() {
		if p.MasterProfile.SinglePlacementGroup == nil {
			p.MasterProfile.SinglePlacementGroup = to.BoolPtr(DefaultSinglePlacementGroup)
		}
	}

	if p.MasterProfile.IsCustomVNET() && p.MasterProfile.IsVirtualMachineScaleSets() {
		if p.OrchestratorProfile.OrchestratorType == Kubernetes {
			p.MasterProfile.FirstConsecutiveStaticIP = p.MasterProfile.GetFirstConsecutiveStaticIPAddress(p.MasterProfile.VnetCidr)
		}
	}

	if !p.OrchestratorProfile.IsKubernetes() {
		p.MasterProfile.Distro = Ubuntu
		if !p.MasterProfile.IsCustomVNET() {
			if p.OrchestratorProfile.OrchestratorType == DCOS {
				p.MasterProfile.Subnet = DefaultDCOSMasterSubnet
				// FirstConsecutiveStaticIP is not reset if it is upgrade and some value already exists
				if !isUpgrade || len(p.MasterProfile.FirstConsecutiveStaticIP) == 0 {
					p.MasterProfile.FirstConsecutiveStaticIP = DefaultDCOSFirstConsecutiveStaticIP
				}
			} else if p.HasWindows() {
				p.MasterProfile.Subnet = DefaultSwarmWindowsMasterSubnet
				// FirstConsecutiveStaticIP is not reset if it is upgrade and some value already exists
				if !isUpgrade || len(p.MasterProfile.FirstConsecutiveStaticIP) == 0 {
					p.MasterProfile.FirstConsecutiveStaticIP = DefaultSwarmWindowsFirstConsecutiveStaticIP
				}
			} else {
				p.MasterProfile.Subnet = DefaultMasterSubnet
				// FirstConsecutiveStaticIP is not reset if it is upgrade and some value already exists
				if !isUpgrade || len(p.MasterProfile.FirstConsecutiveStaticIP) == 0 {
					p.MasterProfile.FirstConsecutiveStaticIP = DefaultFirstConsecutiveStaticIP
				}
			}
		}
	}

	if p.MasterProfile.HTTPSourceAddressPrefix == "" {
		p.MasterProfile.HTTPSourceAddressPrefix = "*"
	}

	if nil == p.MasterProfile.CosmosEtcd {
		p.MasterProfile.CosmosEtcd = to.BoolPtr(DefaultUseCosmos)
	}

	if p.MasterProfile.PlatformUpdateDomainCount == nil {
		p.MasterProfile.PlatformUpdateDomainCount = to.IntPtr(3)
	}
}

func (cs *ContainerService) setLoadBalancerSkuDefaults() {
	p := cs.Properties

	if p.OrchestratorProfile == nil {
		p.OrchestratorProfile = &OrchestratorProfile{}
	}

	if p.OrchestratorProfile.KubernetesConfig == nil {
		p.OrchestratorProfile.KubernetesConfig = &KubernetesConfig{}
	}

	if p.OrchestratorProfile.KubernetesConfig.LoadBalancerSku == "" {
		if p.HasAvailabilityZones() {
			p.OrchestratorProfile.KubernetesConfig.LoadBalancerSku = StandardLoadBalancerSku
		} else {
			p.OrchestratorProfile.KubernetesConfig.LoadBalancerSku = DefaultLoadBalancerSku
		}
	}

	// normalize sku
	if strings.EqualFold(p.OrchestratorProfile.KubernetesConfig.LoadBalancerSku, BasicLoadBalancerSku) {
		p.OrchestratorProfile.KubernetesConfig.LoadBalancerSku = BasicLoadBalancerSku
	} else if strings.EqualFold(p.OrchestratorProfile.KubernetesConfig.LoadBalancerSku, StandardLoadBalancerSku) {
		p.OrchestratorProfile.KubernetesConfig.LoadBalancerSku = StandardLoadBalancerSku
	}
}

func (cs *ContainerService) setAgentProfileDefaults(isUpgrade, isScale bool) {
	p := cs.Properties

	for _, profile := range p.AgentPoolProfiles {
		if profile.AvailabilityProfile == "" {
			profile.AvailabilityProfile = VirtualMachineScaleSets
		}
		if profile.AvailabilityProfile == VirtualMachineScaleSets {
			if profile.ScaleSetEvictionPolicy == "" && (profile.ScaleSetPriority == ScaleSetPriorityLow || profile.ScaleSetPriority == ScaleSetPrioritySpot) {
				profile.ScaleSetEvictionPolicy = ScaleSetEvictionPolicyDelete
			}

			if profile.ScaleSetPriority == ScaleSetPrioritySpot && profile.SpotMaxPrice == nil {
				var maximumValueFlag float64 = -1
				profile.SpotMaxPrice = &maximumValueFlag
			}

			if profile.VMSSOverProvisioningEnabled == nil {
				profile.VMSSOverProvisioningEnabled = to.BoolPtr(DefaultVMSSOverProvisioningEnabled && !isUpgrade && !isScale)
			}

			if profile.SinglePlacementGroup == nil {
				if strings.EqualFold(p.OrchestratorProfile.KubernetesConfig.LoadBalancerSku, StandardLoadBalancerSku) {
					profile.SinglePlacementGroup = to.BoolPtr(false)
				} else {
					profile.SinglePlacementGroup = to.BoolPtr(DefaultSinglePlacementGroup)
				}
			}
		}
		// set default OSType to Linux
		if profile.OSType == "" {
			profile.OSType = Linux
		}

		if profile.PlatformUpdateDomainCount == nil {
			profile.PlatformUpdateDomainCount = to.IntPtr(3)
		}

		// Accelerated Networking is supported on most general purpose and compute-optimized instance sizes with 2 or more vCPUs.
		// These supported series are: D/DSv2 and F/Fs // All the others are not supported
		// On instances that support hyperthreading, Accelerated Networking is supported on VM instances with 4 or more vCPUs.
		// Supported series are: D/DSv3, E/ESv3, Fsv2, and Ms/Mms.
		if profile.AcceleratedNetworkingEnabled == nil {
			profile.AcceleratedNetworkingEnabled = to.BoolPtr(DefaultAcceleratedNetworking && !isUpgrade && !isScale && helpers.AcceleratedNetworkingSupported(profile.VMSize))
		}

		if profile.AcceleratedNetworkingEnabledWindows == nil {
			profile.AcceleratedNetworkingEnabledWindows = to.BoolPtr(DefaultAcceleratedNetworkingWindowsEnabled && !isUpgrade && !isScale && helpers.AcceleratedNetworkingSupported(profile.VMSize))
		}

		if profile.AuditDEnabled == nil {
			profile.AuditDEnabled = to.BoolPtr(DefaultAuditDEnabled && !isUpgrade && !isScale)
		}

		if profile.PreserveNodesProperties == nil {
			profile.PreserveNodesProperties = to.BoolPtr(DefaultPreserveNodesProperties)
		}

		if profile.EnableVMSSNodePublicIP == nil {
			profile.EnableVMSSNodePublicIP = to.BoolPtr(DefaultEnableVMSSNodePublicIP)
		}

		if !p.OrchestratorProfile.IsKubernetes() {
			profile.Distro = Ubuntu
		}
	}
}

// setStorageDefaults for agents
func (cs *ContainerService) setStorageDefaults() {
	p := cs.Properties
	if p.MasterProfile != nil && len(p.MasterProfile.StorageProfile) == 0 {
		if p.OrchestratorProfile.OrchestratorType == Kubernetes {
			p.MasterProfile.StorageProfile = ManagedDisks
		} else {
			p.MasterProfile.StorageProfile = StorageAccount
		}
	}
	for _, profile := range p.AgentPoolProfiles {
		if len(profile.StorageProfile) == 0 {
			if p.OrchestratorProfile.OrchestratorType == Kubernetes {
				profile.StorageProfile = ManagedDisks
			} else {
				profile.StorageProfile = StorageAccount
			}
		}
	}
}

func (cs *ContainerService) setExtensionDefaults() {
	p := cs.Properties
	if p.ExtensionProfiles == nil {
		return
	}
	for _, extension := range p.ExtensionProfiles {
		if extension.RootURL == "" {
			extension.RootURL = DefaultExtensionsRootURL
		}
	}
}

func (cs *ContainerService) setHostedMasterProfileDefaults() {
	cs.Properties.HostedMasterProfile.Subnet = DefaultKubernetesMasterSubnet
}

// setWindowsProfileDefaults sets default WindowsProfile values
func (cs *ContainerService) setWindowsProfileDefaults(isUpgrade, isScale bool) {
	p := cs.Properties
	windowsProfile := p.WindowsProfile
	if !isUpgrade && !isScale {
		if windowsProfile.SSHEnabled == nil {
			windowsProfile.SSHEnabled = to.BoolPtr(DefaultWindowsSSHEnabled)
		}

		// This allows caller to use the latest ImageVersion and WindowsSku for adding a new Windows pool to an existing cluster.
		// We must assure that same WindowsPublisher and WindowsOffer are used in an existing cluster.
		if windowsProfile.WindowsPublisher == AKSWindowsServer2019OSImageConfig.ImagePublisher && windowsProfile.WindowsOffer == AKSWindowsServer2019OSImageConfig.ImageOffer {
			if windowsProfile.WindowsSku == "" {
				windowsProfile.WindowsSku = AKSWindowsServer2019OSImageConfig.ImageSku
			}
			if windowsProfile.ImageVersion == "" {
				if windowsProfile.WindowsSku == AKSWindowsServer2019OSImageConfig.ImageSku {
					windowsProfile.ImageVersion = AKSWindowsServer2019OSImageConfig.ImageVersion
				} else {
					windowsProfile.ImageVersion = "latest"
				}
			}
		} else if windowsProfile.WindowsPublisher == WindowsServer2019OSImageConfig.ImagePublisher && windowsProfile.WindowsOffer == WindowsServer2019OSImageConfig.ImageOffer {
			if windowsProfile.WindowsSku == "" {
				windowsProfile.WindowsSku = WindowsServer2019OSImageConfig.ImageSku
			}
			if windowsProfile.ImageVersion == "" {
				if windowsProfile.WindowsSku == WindowsServer2019OSImageConfig.ImageSku {
					windowsProfile.ImageVersion = WindowsServer2019OSImageConfig.ImageVersion
				} else {
					windowsProfile.ImageVersion = "latest"
				}
			}
		} else {
			if windowsProfile.WindowsPublisher == "" {
				windowsProfile.WindowsPublisher = AKSWindowsServer2019OSImageConfig.ImagePublisher
			}
			if windowsProfile.WindowsOffer == "" {
				windowsProfile.WindowsOffer = AKSWindowsServer2019OSImageConfig.ImageOffer
			}
			if windowsProfile.WindowsSku == "" {
				windowsProfile.WindowsSku = AKSWindowsServer2019OSImageConfig.ImageSku
			}

			if windowsProfile.ImageVersion == "" {
				// default versions are specific to a publisher/offer/sku
				if windowsProfile.WindowsPublisher == AKSWindowsServer2019OSImageConfig.ImagePublisher && windowsProfile.WindowsOffer == AKSWindowsServer2019OSImageConfig.ImageOffer && windowsProfile.WindowsSku == AKSWindowsServer2019OSImageConfig.ImageSku {
					windowsProfile.ImageVersion = AKSWindowsServer2019OSImageConfig.ImageVersion
				} else {
					windowsProfile.ImageVersion = "latest"
				}
			}
		}
	} else if isUpgrade {
		// Image reference publisher and offer only can be set when you create the scale set so we keep the old values.
		// Reference: https://docs.microsoft.com/en-us/azure/virtual-machine-scale-sets/virtual-machine-scale-sets-upgrade-scale-set#create-time-properties
		if windowsProfile.WindowsPublisher == AKSWindowsServer2019OSImageConfig.ImagePublisher && windowsProfile.WindowsOffer == AKSWindowsServer2019OSImageConfig.ImageOffer {
			if windowsProfile.ImageVersion == "" {
				windowsProfile.ImageVersion = AKSWindowsServer2019OSImageConfig.ImageVersion
			}
			if windowsProfile.WindowsSku == "" {
				windowsProfile.WindowsSku = AKSWindowsServer2019OSImageConfig.ImageSku
			}
		} else if windowsProfile.WindowsPublisher == WindowsServer2019OSImageConfig.ImagePublisher && windowsProfile.WindowsOffer == WindowsServer2019OSImageConfig.ImageOffer {
			if windowsProfile.ImageVersion == "" {
				windowsProfile.ImageVersion = WindowsServer2019OSImageConfig.ImageVersion
			}
			if windowsProfile.WindowsSku == "" {
				windowsProfile.WindowsSku = WindowsServer2019OSImageConfig.ImageSku
			}
		}
	}
	// Scale: Keep the same version to match other nodes because we have no way to rollback
}

func (cs *ContainerService) setTelemetryProfileDefaults() {
	p := cs.Properties
	if p.TelemetryProfile == nil {
		p.TelemetryProfile = &TelemetryProfile{}
	}

	if len(p.TelemetryProfile.ApplicationInsightsKey) == 0 {
		p.TelemetryProfile.ApplicationInsightsKey = DefaultApplicationInsightsKey
	}
}