package kube

import (
	"fmt"
	"strings"
)

// validateObjectReference is the whitespace rule every Console read shares. It
// names the object the caller asked for, because an error that does not say
// what was rejected cannot be acted on.
func validateObjectReference(subject, namespace, name string) error {
	if strings.TrimSpace(namespace) == "" || strings.TrimSpace(namespace) != namespace ||
		strings.TrimSpace(name) == "" || strings.TrimSpace(name) != name {
		return fmt.Errorf("%s namespace and name are required without surrounding whitespace", subject)
	}
	return nil
}
