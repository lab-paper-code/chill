package state

import (
	serving "github.com/lab-paper-code/chill/internal/serving"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

const (
	PowerChangedReason = "PowerChangedDuringMeasurement"
)

func Refused(code, message string) error { return &serving.Error{Code: code, Message: message} }

func IgnoreNotFound(err error) error {
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}
