package profilingrun

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

type candidateReport struct {
	Scope  string `json:"scope"`
	Inputs struct {
		ModelSpec struct {
			ContentDigest string `json:"contentDigest"`
		} `json:"modelSpec"`
		DeviceClass struct {
			ContentDigest string `json:"contentDigest"`
		} `json:"deviceClass"`
		RuntimeDeclaration struct {
			ContentDigest string `json:"contentDigest"`
			Image         string `json:"image"`
		} `json:"runtimeDeclaration"`
	} `json:"inputs"`
	Selection struct {
		ExecutionPath  string `json:"executionPath"`
		Artifact       string `json:"artifact"`
		ArtifactDigest string `json:"artifactDigest"`
	} `json:"selection"`
	Verdict string `json:"verdict"`
}

func ValidateCandidateReport(payload []byte, intent Intent) error {
	sum := sha256.Sum256(payload)
	actual := "sha256:" + hex.EncodeToString(sum[:])
	if actual != intent.Candidate.ReportDigest {
		return fmt.Errorf("candidate report digest mismatch: %s", actual)
	}
	var report candidateReport
	if err := json.Unmarshal(payload, &report); err != nil {
		return fmt.Errorf("decode candidate report: %w", err)
	}
	checks := []struct{ name, want, got string }{
		{"scope", "StaticCompatibility", report.Scope},
		{"verdict", intent.Candidate.Verdict, report.Verdict},
		{"ModelSpec digest", intent.Candidate.ModelSpecContentDigest, report.Inputs.ModelSpec.ContentDigest},
		{"DeviceClass digest", intent.Candidate.DeviceClassContentDigest, report.Inputs.DeviceClass.ContentDigest},
		{
			"runtime declaration digest",
			intent.Candidate.RuntimeDeclarationDigest,
			report.Inputs.RuntimeDeclaration.ContentDigest,
		},
		{"runtime image", intent.Images.Runtime, report.Inputs.RuntimeDeclaration.Image},
		{"execution path", intent.Candidate.ExecutionPath, report.Selection.ExecutionPath},
		{"artifact", intent.State.Artifact, report.Selection.Artifact},
		{"artifact digest", intent.State.ArtifactDigest, report.Selection.ArtifactDigest},
	}
	for _, check := range checks {
		if check.want != check.got {
			return fmt.Errorf("candidate %s mismatch", check.name)
		}
	}
	return nil
}
