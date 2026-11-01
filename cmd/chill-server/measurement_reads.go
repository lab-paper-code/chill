package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/operatorconsole"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
)

func consoleReadHandler(origin *url.URL, measurement *measurementService) http.Handler {
	if origin == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if measurement != nil && isMeasurementResult(r) {
				measurement.writeMeasurementResultError(w, r, http.StatusServiceUnavailable, consoleUnavailableCode)
				return
			}
			consoleError(w, http.StatusServiceUnavailable, consoleUnavailableCode)
		})
	}
	return &httputil.ReverseProxy{
		Rewrite: func(proxy *httputil.ProxyRequest) {
			proxy.Out.URL.Scheme, proxy.Out.URL.Host = origin.Scheme, origin.Host
			proxy.Out.Host = origin.Host
			// Allowlist, rather than chasing every forged identity header.
			proxy.Out.Header = make(http.Header)
			proxy.Out.URL.User = nil
			proxy.Out.URL.RawQuery = proxy.In.URL.RawQuery
			for _, key := range []string{"Accept", "Content-Type"} {
				if value := proxy.In.Header.Get(key); value != "" {
					proxy.Out.Header.Set(key, value)
				}
			}
			proxy.Out.Header.Set("User-Agent", "")
		},
		// ReverseProxy uses RoundTrip directly: redirects are never followed.
		ModifyResponse: func(response *http.Response) error {
			response.Header.Set("Cache-Control", "no-store")
			response.Header.Del("Set-Cookie")
			if measurement != nil && response.StatusCode == http.StatusOK && isMeasurementPreflight(response.Request) {
				return measurement.enrichPreflightResponse(response)
			}
			if measurement != nil {
				return measurement.enrichMeasurementResultError(response)
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, _ error) {
			status, code := http.StatusBadGateway, consoleUnavailableCode
			if errors.Is(r.Context().Err(), context.DeadlineExceeded) {
				status, code = http.StatusGatewayTimeout, "ConsoleTimeout"
			}
			if measurement != nil && isMeasurementResult(r) {
				measurement.writeMeasurementResultError(w, r, status, code)
				return
			}
			consoleError(w, status, code)
		},
	}
}

// Pending evidence is a native execution state, not a failed Evidence Store read.
// Only current, progressing Runs without a published reference take this path;
// published, invalid, failed and stale evidence still goes to the verified reader.
func (o *measurementService) servePendingPassport(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/evidence-passport") {
		return false
	}
	namespace, name := r.PathValue("namespace"), r.PathValue("name")
	if err := o.namespace(namespace); err != nil {
		consoleError(w, http.StatusForbidden, "Forbidden")
		return true
	}
	var run api.ProfilingRun
	if err := o.client.Get(r.Context(), types.NamespacedName{Namespace: namespace, Name: name}, &run); err != nil {
		status := http.StatusServiceUnavailable
		if apierrors.IsNotFound(err) {
			status = http.StatusNotFound
		} else if apierrors.IsForbidden(err) {
			status = http.StatusForbidden
		}
		consoleError(w, status, "EvidencePassportUnavailable")
		return true
	}
	assessment := operatorconsole.AssessProfilingRun(run)
	if run.Status.Evidence != nil || assessment.DisplayStatus != operatorconsole.MeasurementRunning {
		return false
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"apiVersion": operatorconsole.EvidencePassportAPIVersion, "kind": "EvidencePassport",
		"readAt": time.Now().UTC(), "code": "EvidencePassportPending",
		"run": operatorconsole.EvidencePassportRun{
			Namespace: run.Namespace, Name: run.Name, UID: string(run.UID), ResourceVersion: run.ResourceVersion,
			Generation: run.Generation, Spec: run.Spec, Conditions: run.Status.Conditions,
			Job: run.Status.Job, ActualExecution: run.Status.ObservedExecution,
		},
		"assessment": assessment, "evidence": nil, "measurement": nil,
	})
	return true
}

func isMeasurementResult(r *http.Request) bool {
	return r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/result") &&
		strings.Contains(r.URL.Path, "/batch-energy-optimizations/")
}

// Evidence errors keep their original HTTP status and code. Native request
// metadata is read independently, so an unreadable result cannot hide a failed
// request's conditions, identity or progress. No stored result is synthesized.
func (o *measurementService) addMeasurementResultMetadata(r *http.Request, status int, body map[string]any) {
	body["evidenceError"] = map[string]any{"code": body["code"], "status": status}
	namespace, name := r.PathValue("namespace"), r.PathValue("name")
	if err := o.namespace(namespace); err != nil {
		body["metadataError"] = map[string]any{"code": "Forbidden"}
		return
	}
	var task api.BatchEnergyOptimization
	if err := o.client.Get(r.Context(), types.NamespacedName{Namespace: namespace, Name: name}, &task); err != nil {
		body["metadataError"] = map[string]any{"code": "MeasurementMetadataUnavailable"}
		return
	}
	var stored *evidencestore.ObjectIdentity
	if ref := task.Status.Result; ref != nil {
		stored = &evidencestore.ObjectIdentity{Store: ref.Store, Key: ref.Key, Size: ref.Size}
	}
	assessment := operatorconsole.AssessMeasurement(operatorconsole.MeasurementAssessmentInput{
		Execution: &operatorconsole.MeasurementExecution{
			Phase: task.Status.Phase, Generation: task.Generation, ObservedGeneration: task.Status.ObservedGeneration,
		},
		Attempted: true, SourcesComplete: false,
	})
	assessment.FailureSummary = operatorconsole.MeasurementRequestFailures(task, nil, assessment)
	body["apiVersion"] = operatorconsole.BatchEnergyOptimizationResultAPIVersion
	body["kind"] = "BatchEnergyOptimizationResult"
	body["readAt"] = time.Now().UTC()
	body["campaign"] = operatorconsole.BatchEnergyOptimizationResultCampaign{
		Namespace: task.Namespace, Name: task.Name, UID: string(task.UID), ResourceVersion: task.ResourceVersion,
		Generation: task.Generation, Spec: task.Spec, Phase: task.Status.Phase, Outcome: task.Status.Outcome,
		CompletedTrials: task.Status.CompletedTrials, Conditions: task.Status.Conditions, StoredResult: stored,
	}
	body["assessment"], body["result"] = assessment, nil
	body["statistics"], body["measurementFacts"] = nil, nil
	body["statisticsReason"], body["measurementFactsReason"] = "ResultUnavailable", "ResultUnavailable"
	body["resultRows"] = []operatorconsole.ResultRow{}
}

func (o *measurementService) enrichMeasurementResultError(response *http.Response) error {
	if response.StatusCode < http.StatusInternalServerError || !isMeasurementResult(response.Request) {
		return nil
	}
	const maximumErrorBytes = 1 << 20
	payload, err := io.ReadAll(io.LimitReader(response.Body, maximumErrorBytes+1))
	closeErr := response.Body.Close()
	if errors.Join(err, closeErr) != nil || len(payload) > maximumErrorBytes {
		return errors.New("read Console measurement error response")
	}
	response.Body = io.NopCloser(bytes.NewReader(payload))
	var body map[string]any
	if json.Unmarshal(payload, &body) != nil || body["code"] == nil {
		return nil
	}
	o.addMeasurementResultMetadata(response.Request, response.StatusCode, body)
	payload, err = json.Marshal(body)
	if err != nil {
		return err
	}
	response.Body = io.NopCloser(bytes.NewReader(payload))
	response.ContentLength = int64(len(payload))
	response.Header.Set("Content-Length", strconv.Itoa(len(payload)))
	return nil
}

func (o *measurementService) writeMeasurementResultError(
	w http.ResponseWriter, r *http.Request, status int, code string,
) {
	body := map[string]any{"code": code}
	o.addMeasurementResultMetadata(r, status, body)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
