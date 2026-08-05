package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/test/utils"
)

const namespace = "chill-system"

var _ = Describe("operator", Ordered, func() {
	BeforeAll(func() {
		By("creating operator namespace")
		cmd := exec.Command("kubectl", "create", "ns", namespace)
		_, _ = utils.Run(cmd)
	})

	AfterAll(func() {
		By("undeploying the operator")
		cmd := exec.Command("make", "undeploy", "ignore-not-found=true")
		_, _ = utils.Run(cmd)

		By("uninstalling CHILL CRDs")
		cmd = exec.Command("make", "uninstall", "ignore-not-found=true")
		_, _ = utils.Run(cmd)

		By("removing operator namespace")
		cmd = exec.Command("kubectl", "delete", "ns", namespace, "--ignore-not-found=true")
		_, _ = utils.Run(cmd)
	})

	Context("Operator", func() {
		It("should run successfully", func() {
			var operatorPodName string
			var err error

			projectImage := os.Getenv("IMG")
			if projectImage == "" {
				projectImage = "chill-operator:e2e"
			}

			By("building the operator image")
			cmd := exec.Command("make", "docker-build", fmt.Sprintf("IMG=%s", projectImage))
			_, err = utils.Run(cmd)
			ExpectWithOffset(1, err).NotTo(HaveOccurred())

			By("loading the operator image on Kind")
			err = utils.LoadImageToKindClusterWithName(projectImage)
			ExpectWithOffset(1, err).NotTo(HaveOccurred())

			By("installing CRDs")
			cmd = exec.Command("make", "install")
			_, err = utils.Run(cmd)
			ExpectWithOffset(1, err).NotTo(HaveOccurred())

			By("deploying the operator")
			cmd = exec.Command("make", "deploy", fmt.Sprintf("IMG=%s", projectImage))
			_, err = utils.Run(cmd)
			ExpectWithOffset(1, err).NotTo(HaveOccurred())

			By("validating that the operator pod is running as expected")
			verifyOperatorUp := func() error {
				// Get pod name

				cmd = exec.Command("kubectl", "get",
					"pods", "-l", "app.kubernetes.io/component=operator",
					"-o", "go-template={{ range .items }}"+
						"{{ if not .metadata.deletionTimestamp }}"+
						"{{ .metadata.name }}"+
						"{{ \"\\n\" }}{{ end }}{{ end }}",
					"-n", namespace,
				)

				podOutput, err := utils.Run(cmd)
				ExpectWithOffset(2, err).NotTo(HaveOccurred())
				podNames := utils.GetNonEmptyLines(string(podOutput))
				if len(podNames) != 1 {
					return fmt.Errorf("expect 1 operator pod running, but got %d", len(podNames))
				}
				operatorPodName = podNames[0]
				ExpectWithOffset(2, operatorPodName).Should(ContainSubstring("operator"))

				// Validate pod status
				cmd = exec.Command("kubectl", "get",
					"pods", operatorPodName, "-o", "jsonpath={.status.phase}",
					"-n", namespace,
				)
				status, err := utils.Run(cmd)
				ExpectWithOffset(2, err).NotTo(HaveOccurred())
				if string(status) != "Running" {
					return fmt.Errorf("operator pod in %s status", status)
				}
				return nil
			}
			EventuallyWithOffset(1, verifyOperatorUp, time.Minute, time.Second).Should(Succeed())

		})

		It("garbage-collects a ProfilingRun-owned Job when the Run is deleted", func() {
			testContext, cancel := context.WithTimeout(context.Background(), time.Minute)
			DeferCleanup(cancel)
			kube := newE2EClient()
			suffix := time.Now().UnixNano()
			namespaceName := fmt.Sprintf("profilingrun-gc-%d", suffix)
			runName := fmt.Sprintf("profilingrun-gc-%d", suffix)
			jobName := runName + "-measurement"

			testNamespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespaceName}}
			Expect(kube.Create(testContext, testNamespace)).To(Succeed())
			DeferCleanup(func() {
				cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), time.Minute)
				defer cleanupCancel()
				_ = kube.Delete(cleanupContext, testNamespace)
			})

			run := e2eProfilingRun(namespaceName, runName)
			Expect(kube.Create(testContext, run)).To(Succeed())
			job := activeOwnedJob(namespaceName, jobName)
			Expect(controllerutil.SetControllerReference(run, job, kube.Scheme())).To(Succeed())
			Expect(kube.Create(testContext, job)).To(Succeed())
			Expect(metav1.IsControlledBy(job, run)).To(BeTrue())
			Eventually(func() error {
				pods := &corev1.PodList{}
				if err := kube.List(
					testContext,
					pods,
					client.InNamespace(namespaceName),
					client.MatchingLabels{"job-name": jobName},
				); err != nil {
					return err
				}
				if len(pods.Items) != 1 || pods.Items[0].Status.Phase != corev1.PodRunning {
					return fmt.Errorf("active Job Pods = %#v, want one Running Pod", pods.Items)
				}
				return nil
			}, 30*time.Second, 250*time.Millisecond).Should(Succeed())

			By("deleting the Run and waiting for native Job and Pod garbage collection")
			Expect(kube.Delete(
				testContext,
				run,
				client.PropagationPolicy(metav1.DeletePropagationBackground),
			)).To(Succeed())
			Eventually(func() error {
				err := kube.Get(
					testContext,
					client.ObjectKey{Namespace: namespaceName, Name: jobName},
					&batchv1.Job{},
				)
				if err != nil && !apierrors.IsNotFound(err) {
					return err
				}
				if err == nil {
					return fmt.Errorf("Job %s/%s still exists", namespaceName, jobName)
				}
				pods := &corev1.PodList{}
				if err := kube.List(
					testContext,
					pods,
					client.InNamespace(namespaceName),
					client.MatchingLabels{"job-name": jobName},
				); err != nil {
					return err
				}
				if len(pods.Items) != 0 {
					return fmt.Errorf("Job was collected but %d owned Pods remain", len(pods.Items))
				}
				return nil
			}, 30*time.Second, 250*time.Millisecond).Should(Succeed())
		})
	})
})

func newE2EClient() client.Client {
	config, err := ctrl.GetConfig()
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	scheme := runtime.NewScheme()
	ExpectWithOffset(1, clientgoscheme.AddToScheme(scheme)).To(Succeed())
	ExpectWithOffset(1, edgev1alpha1.AddToScheme(scheme)).To(Succeed())
	kube, err := client.New(config, client.Options{Scheme: scheme})
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	return kube
}

func e2eProfilingRun(namespaceName, name string) *edgev1alpha1.ProfilingRun {
	return &edgev1alpha1.ProfilingRun{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespaceName, Name: name},
		Spec: edgev1alpha1.ProfilingRunSpec{
			Candidate: edgev1alpha1.ProfilingRunCandidateSpec{
				ModelSpec: "gc-fixture", DeviceClass: "gc-fixture",
				ExecutionPath: "gc-fixture", PowerMode: "fixed",
			},
			TargetNode: "gc-fixture", CPURequest: resource.MustParse("1m"),
			Measurement: edgev1alpha1.ProfilingRunMeasurementSpec{
				Batch: 1, WarmupIterations: 1, DurationSeconds: 1, Repetitions: 1,
				WorkloadMode:     "saturated-serial-fixed-batch",
				InputGenerator:   "numpy-default-rng-seed-0-fp32-v1",
				OutputValidation: "nonempty-shape-batch-v1",
			},
			MetricObservation: edgev1alpha1.ProfilingRunMetricObservationSpec{
				IntervalMilliseconds: 2, TimeoutMilliseconds: 1, DurationSeconds: 1,
			},
			AdmissionPolicy: "gc-fixture",
		},
	}
}

func activeOwnedJob(namespaceName, name string) *batchv1.Job {
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespaceName, Name: name},
		Spec: batchv1.JobSpec{
			BackoffLimit: ptr.To(int32(0)),
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				RestartPolicy: corev1.RestartPolicyNever,
				Containers: []corev1.Container{{
					Name: "fixture", Image: "registry.k8s.io/pause:3.10",
				}},
			}},
		},
	}
}
