package metadata

// NodeDiscoveryLabelKeys returns labels written by node-discovery host probing.
func NodeDiscoveryLabelKeys() []string {
	return []string{
		DeviceVendor,
		DeviceFamily,
		DeviceModel,
		Accelerator,
	}
}

// NodeDiscoveryAnnotationKeys returns annotations written by node-discovery host probing.
func NodeDiscoveryAnnotationKeys() []string {
	return []string{
		DeviceModelRaw,
		DiscoverySource,
		NodeDiscoveryResult,
		NodeDiscoveryReason,
	}
}

// DeviceDiscoveryAnnotationKeys returns annotations written by DeviceClass discovery.
func DeviceDiscoveryAnnotationKeys() []string {
	return []string{
		DeviceClassDiscoveryResult,
		DeviceClassDiscoveryReason,
		DeviceClassDiscoveryClass,
	}
}
