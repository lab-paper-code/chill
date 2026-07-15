package evidencestore

import (
	"errors"
	"net"
	"regexp"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

var (
	digestPinnedImagePattern = regexp.MustCompile(`^[^@[:space:]]+@sha256:[0-9a-f]{64}$`)
	bucketNamePattern        = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*[a-z0-9]$`)
)

// Config is the rendered Evidence Store operand contract read by the operator.
type Config struct {
	Image              string                      `json:"image"`
	BootstrapImage     string                      `json:"bootstrapImage"`
	ImagePullPolicy    corev1.PullPolicy           `json:"imagePullPolicy"`
	Bucket             string                      `json:"bucket"`
	Region             string                      `json:"region"`
	StorageClassName   string                      `json:"storageClassName,omitempty"`
	StorageSize        resource.Quantity           `json:"storageSize"`
	Resources          corev1.ResourceRequirements `json:"resources,omitempty"`
	BootstrapResources corev1.ResourceRequirements `json:"bootstrapResources,omitempty"`
}

func (c Config) Validate() error {
	var problems []string
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "image", value: c.Image},
		{name: "bootstrapImage", value: c.BootstrapImage},
		{name: "imagePullPolicy", value: string(c.ImagePullPolicy)},
		{name: "bucket", value: c.Bucket},
		{name: "region", value: c.Region},
	} {
		if strings.TrimSpace(field.value) == "" {
			problems = append(problems, field.name+" is required")
		}
	}

	for _, image := range []struct {
		name  string
		value string
	}{
		{name: "image", value: c.Image},
		{name: "bootstrapImage", value: c.BootstrapImage},
	} {
		if strings.TrimSpace(image.value) != "" && !digestPinnedImagePattern.MatchString(image.value) {
			problems = append(problems, image.name+" must be repository@sha256")
		}
	}

	if strings.TrimSpace(string(c.ImagePullPolicy)) != "" {
		switch c.ImagePullPolicy {
		case corev1.PullAlways, corev1.PullIfNotPresent, corev1.PullNever:
		default:
			problems = append(problems, "imagePullPolicy must be Always, IfNotPresent, or Never")
		}
	}
	if bucket := strings.TrimSpace(c.Bucket); bucket != "" && !validBucketName(bucket) {
		problems = append(problems, "bucket must be a valid S3 bucket name")
	}
	if c.StorageSize.Sign() <= 0 {
		problems = append(problems, "storageSize must be positive")
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

func validBucketName(name string) bool {
	if len(name) < 3 || len(name) > 63 || !bucketNamePattern.MatchString(name) {
		return false
	}
	if strings.Contains(name, "..") || strings.Contains(name, ".-") || strings.Contains(name, "-.") {
		return false
	}
	return net.ParseIP(name) == nil
}
