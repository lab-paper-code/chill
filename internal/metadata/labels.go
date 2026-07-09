package metadata

const (
	Prefix = "edge.dacs.io"

	DeviceClass  = Prefix + "/device-class"
	DeviceVendor = Prefix + "/device-vendor"
	DeviceFamily = Prefix + "/device-family"
	DeviceModel  = Prefix + "/device-model"
	Accelerator  = Prefix + "/accelerator"

	System          = Prefix + "/system"
	ManagedBy       = Prefix + "/managed-by"
	DiscoverySource = Prefix + "/discovery-source"
	DeviceModelRaw  = Prefix + "/device-model-raw"

	NodeDiscoveryResult        = Prefix + "/node-discovery-result"
	NodeDiscoveryReason        = Prefix + "/node-discovery-reason"
	DeviceClassDiscoveryResult = Prefix + "/device-class-discovery-result"
	DeviceClassDiscoveryReason = Prefix + "/device-class-discovery-reason"
	DeviceClassDiscoveryClass  = Prefix + "/device-class-discovery-class"
)

const (
	ManagedByDeviceDiscovery = "chill-discovery"
	SourceNode               = "node"
	SourceNodeDiscovery      = "chill-node-discovery"
)

const (
	DiscoveryResultMatched   = "matched"
	DiscoveryResultUnmatched = "unmatched"

	DiscoveryReasonSignatureMatched     = "signature-matched"
	DiscoveryReasonNoSignatureMatch     = "no-signature-match"
	DiscoveryReasonNoSourceFacts        = "no-source-facts"
	DiscoveryReasonCatalogMatched       = "catalog-matched"
	DiscoveryReasonNoCatalogMatch       = "no-catalog-match"
	DiscoveryReasonCatalogEmpty         = "catalog-empty"
	DiscoveryReasonManualLabelPreserved = "manual-label-preserved"
)
