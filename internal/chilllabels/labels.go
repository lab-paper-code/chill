package chilllabels

const (
	Prefix = "edge.dacs.io"

	DeviceClass  = Prefix + "/device-class"
	DeviceVendor = Prefix + "/device-vendor"
	DeviceFamily = Prefix + "/device-family"
	DeviceModel  = Prefix + "/device-model"
	Accelerator  = Prefix + "/accelerator"

	ManagedBy       = Prefix + "/managed-by"
	DiscoverySource = Prefix + "/discovery-source"
	DeviceModelRaw  = Prefix + "/device-model-raw"
)

const (
	ManagedByDeviceDiscovery = "chill-discovery"
	SourceNode               = "node"
	SourceNodeDiscovery      = "chill-node-discovery"
)
