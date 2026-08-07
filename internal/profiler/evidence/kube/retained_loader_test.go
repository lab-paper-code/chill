package kube

import (
	"context"
	"errors"
	"strings"
	"testing"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/component"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	storekube "github.com/lab-paper-code/chill/internal/evidencestore/kube"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiler/evidence"
	"github.com/lab-paper-code/chill/internal/profilingrun"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestRetainedLoaderUsesRecordedJobBindingAndCredentials(t *testing.T) {
	t.Parallel()
	run, job, secret, reference := retainedLoaderFixture()
	want := errors.New("reader unavailable")
	var gotBinding storekube.Binding
	var gotCredentials storekube.Credentials
	loader, err := NewRetainedLoader(
		fake.NewClientBuilder().WithScheme(retainedLoaderScheme(t)).WithObjects(job, secret).Build(),
		func(binding storekube.Binding, credentials storekube.Credentials) (evidencestore.LocationReader, error) {
			gotBinding, gotCredentials = binding, credentials
			return nil, want
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = loader.Load(context.Background(), run, reference)
	if !errors.Is(err, want) {
		t.Fatalf("Load() error = %v", err)
	}
	if gotBinding.Store != reference.Store || gotBinding.Endpoint != "http://store:9000" ||
		gotCredentials.AccessKeyID != "access" || gotCredentials.SecretAccessKey != "secret" {
		t.Fatalf("binding/credentials = %#v, %#v", gotBinding, gotCredentials)
	}
}

func TestRetainedLoaderRejectsJobOrStoreIdentityMismatch(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*edgev1alpha1.ProfilingRun, *batchv1.Job, *evidencestore.ObjectIdentity)
		match  string
	}{
		{
			name: "Job UID drift",
			mutate: func(_ *edgev1alpha1.ProfilingRun, job *batchv1.Job, _ *evidencestore.ObjectIdentity) {
				job.UID = "replacement"
			},
			match: "does not match the recorded Run",
		},
		{
			name: "logical store drift",
			mutate: func(_ *edgev1alpha1.ProfilingRun, _ *batchv1.Job, reference *evidencestore.ObjectIdentity) {
				reference.Store = "other-store"
			},
			match: "does not match the retained Job binding",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			run, job, secret, reference := retainedLoaderFixture()
			test.mutate(run, job, &reference)
			loader, err := NewRetainedLoader(
				fake.NewClientBuilder().WithScheme(retainedLoaderScheme(t)).WithObjects(job, secret).Build(),
				func(storekube.Binding, storekube.Credentials) (evidencestore.LocationReader, error) {
					t.Fatal("invalid identity reached store construction")
					return nil, nil
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			_, err = loader.Load(context.Background(), run, reference)
			if err == nil || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("Load() error = %v, want %q", err, test.match)
			}
		})
	}
}

func TestNewRetainedLoaderRequiresDependencies(t *testing.T) {
	t.Parallel()
	reader := fake.NewClientBuilder().WithScheme(retainedLoaderScheme(t)).Build()
	if _, err := NewRetainedLoader(nil, func(storekube.Binding, storekube.Credentials) (evidencestore.LocationReader, error) {
		return nil, nil
	}); err == nil {
		t.Fatal("nil Kubernetes reader was accepted")
	}
	if _, err := NewRetainedLoader(reader, nil); err == nil {
		t.Fatal("nil store factory was accepted")
	}
}

func TestValidateRunEvidenceBindingBindsActuationHandoff(t *testing.T) {
	t.Parallel()
	run := &edgev1alpha1.ProfilingRun{
		ObjectMeta: metav1.ObjectMeta{Namespace: "chill-system", Name: "run", UID: "run-uid"},
		Status: edgev1alpha1.ProfilingRunStatus{
			IntentFrozen: true,
			Job:          &edgev1alpha1.ProfilingRunObjectReference{Name: "job", UID: "job-uid"},
			Actuation:    &edgev1alpha1.ProfilingRunObjectReference{Name: "handoff", UID: "handoff-uid"},
			ActualExecution: &edgev1alpha1.ProfilingRunActualExecutionStatus{
				Pod:  edgev1alpha1.ProfilingRunObjectReference{Name: "pod", UID: "pod-uid"},
				Node: edgev1alpha1.ProfilingRunObjectReference{Name: "node", UID: "node-uid"},
			},
			Evidence: &edgev1alpha1.ProfilingRunEvidenceReference{
				SchemaVersion: profilerevidence.ExecutionEvidenceSchemaVersion,
			},
		},
	}
	execution := profilerevidence.ExecutionEvidence{
		SchemaVersion: profilerevidence.ExecutionEvidenceSchemaVersion,
		Intent: profilingrun.Intent{
			Actuation: &profilingrun.ActuationContract{},
		},
		Terminal: profilerevidence.TerminalFacts{
			Job:  profilerevidence.JobTerminalFacts{Namespace: run.Namespace, Name: "job", UID: "job-uid"},
			Pod:  profilerevidence.PodTerminalFacts{Namespace: run.Namespace, Name: "pod", UID: "pod-uid"},
			Node: profilerevidence.NodeTerminalFacts{Name: "node", UID: "node-uid"},
		},
		Actuation: &profilerevidence.ActuationFacts{
			Run: profilerevidence.NamespacedObjectFacts{
				Namespace: run.Namespace, Name: run.Name, UID: string(run.UID),
			},
			Handoff: profilerevidence.NamespacedObjectFacts{
				Namespace: run.Namespace, Name: "handoff", UID: "handoff-uid",
			},
		},
	}

	if err := validateRunEvidenceBinding(run, execution); err != nil {
		t.Fatalf("validateRunEvidenceBinding() error = %v", err)
	}

	execution.Actuation.Handoff.UID = "replacement-uid"
	if err := validateRunEvidenceBinding(run, execution); err == nil ||
		!strings.Contains(err.Error(), "recorded Run handoff") {
		t.Fatalf("validateRunEvidenceBinding() error = %v, want handoff mismatch", err)
	}
}

func TestValidateRunEvidenceBindingRejectsActuationOnFixedRun(t *testing.T) {
	t.Parallel()
	run := &edgev1alpha1.ProfilingRun{
		ObjectMeta: metav1.ObjectMeta{Namespace: "chill-system", Name: "run", UID: "run-uid"},
		Status: edgev1alpha1.ProfilingRunStatus{
			IntentFrozen: true,
			Job:          &edgev1alpha1.ProfilingRunObjectReference{Name: "job", UID: "job-uid"},
			Actuation:    &edgev1alpha1.ProfilingRunObjectReference{Name: "unexpected", UID: "unexpected-uid"},
			ActualExecution: &edgev1alpha1.ProfilingRunActualExecutionStatus{
				Pod:  edgev1alpha1.ProfilingRunObjectReference{Name: "pod", UID: "pod-uid"},
				Node: edgev1alpha1.ProfilingRunObjectReference{Name: "node", UID: "node-uid"},
			},
			Evidence: &edgev1alpha1.ProfilingRunEvidenceReference{
				SchemaVersion: profilerevidence.ExecutionEvidenceSchemaVersion,
			},
		},
	}
	execution := profilerevidence.ExecutionEvidence{
		SchemaVersion: profilerevidence.ExecutionEvidenceSchemaVersion,
		Terminal: profilerevidence.TerminalFacts{
			Job:  profilerevidence.JobTerminalFacts{Namespace: run.Namespace, Name: "job", UID: "job-uid"},
			Pod:  profilerevidence.PodTerminalFacts{Namespace: run.Namespace, Name: "pod", UID: "pod-uid"},
			Node: profilerevidence.NodeTerminalFacts{Name: "node", UID: "node-uid"},
		},
	}

	if err := validateRunEvidenceBinding(run, execution); err == nil ||
		!strings.Contains(err.Error(), "unexpected actuation identity") {
		t.Fatalf("validateRunEvidenceBinding() error = %v, want fixed Run rejection", err)
	}
}

func retainedLoaderFixture() (
	*edgev1alpha1.ProfilingRun,
	*batchv1.Job,
	*corev1.Secret,
	evidencestore.ObjectIdentity,
) {
	run := &edgev1alpha1.ProfilingRun{
		ObjectMeta: metav1.ObjectMeta{Namespace: "chill-system", Name: "run", UID: "run-uid"},
		Status: edgev1alpha1.ProfilingRunStatus{
			IntentFrozen: true,
			Job:          &edgev1alpha1.ProfilingRunObjectReference{Name: "run-job", UID: "job-uid"},
		},
	}
	secretEnv := func(name, key string) corev1.EnvVar {
		return corev1.EnvVar{Name: name, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: "store-credentials"}, Key: key,
		}}}
	}
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: run.Namespace, Name: run.Status.Job.Name, UID: "job-uid",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: edgev1alpha1.GroupVersion.String(), Kind: "ProfilingRun",
				Name: run.Name, UID: run.UID, Controller: ptr.To(true),
			}},
		},
		Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name: "runtime",
				Args: []string{
					"--store-name=chill-evidence", "--store-endpoint=http://store:9000",
					"--store-bucket=profiling", "--store-region=us-east-1",
				},
				Env: []corev1.EnvVar{
					secretEnv("AWS_ACCESS_KEY_ID", component.EvidenceStoreAccessKeyKey),
					secretEnv("AWS_SECRET_ACCESS_KEY", component.EvidenceStoreSecretKeyKey),
				},
			}},
		}}},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: run.Namespace, Name: "store-credentials"},
		Data: map[string][]byte{
			component.EvidenceStoreAccessKeyKey: []byte("access"),
			component.EvidenceStoreSecretKeyKey: []byte("secret"),
		},
	}
	reference := evidencestore.ObjectIdentity{
		Store: "chill-evidence", Key: "execution.json",
		Size: 100,
	}
	return run, job, secret, reference
}

func retainedLoaderScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{batchv1.AddToScheme, corev1.AddToScheme, edgev1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	return scheme
}
