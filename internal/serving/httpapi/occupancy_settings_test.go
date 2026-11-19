package httpapi

import (
	"context"
	"testing"
	"time"

	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	"github.com/lab-paper-code/chill/internal/serving/observation"
	authentication "k8s.io/api/authentication/v1"
	authorization "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

const (
	settingsTestNamespace = "serving"
	settingsTestConfigMap = "scheduler"
)

func settingsHandler(t *testing.T) *Handler {
	t.Helper()
	log, err := observation.NewObservationLog(
		observation.ObservationLogConfig{
			Directory: cleanObservationDirectory(t),
			Store:     "evidence",
			Retention: time.Hour,
			Writer:    settingsArchive{directory: t.TempDir()},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := log.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	kube := fake.NewSimpleClientset()
	kube.PrependReactor("create", "tokenreviews", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, &authentication.TokenReview{
			Status: authentication.TokenReviewStatus{
				Authenticated: true,
				User:          authentication.UserInfo{Username: "operator", UID: "actor-uid"},
			},
		}, nil
	})
	kube.PrependReactor("create", "subjectaccessreviews", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, &authorization.SubjectAccessReview{
			Status: authorization.SubjectAccessReviewStatus{Allowed: true},
		}, nil
	})
	return &Handler{
		observationLog: log,
		authorizer:     &servingkube.Authorizer{Client: kube},
		config:         HandlerConfig{MaxRequestBytes: 1024},
	}
}
